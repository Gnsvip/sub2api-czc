//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestExcelBPS403RecoveryCustomInterval(t *testing.T) {
	now := time.Now().UTC()
	for _, minutes := range []int{1, 30, 60, 360, 10080} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			a := recoveryTestAccount(now)
			a.Extra[ExcelBPS403RecoveryIntervalMinutesKey] = minutes
			a.Extra[ExcelBPS403DisabledAtKey] = now.Format(time.RFC3339Nano)
			interval := time.Duration(minutes) * time.Minute
			require.False(t, a.ExcelBPS403RecoveryDue(now.Add(interval-time.Nanosecond)))
			require.True(t, a.ExcelBPS403RecoveryDue(now.Add(interval)))
			attempted := now.Add(interval)
			a.Extra[ExcelBPS403LastProbeAtKey] = attempted.Format(time.RFC3339Nano)
			require.False(t, a.ExcelBPS403RecoveryDue(attempted.Add(interval-time.Nanosecond)))
			require.True(t, a.ExcelBPS403RecoveryDue(attempted.Add(interval)))
		})
	}
	a := recoveryTestAccount(now)
	a.Extra[ExcelBPS403LastProbeAtKey] = now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	a.Extra[ExcelBPS403RecoveryIntervalMinutesKey] = 30
	require.True(t, a.ExcelBPS403RecoveryDue(now))
	a.Extra[ExcelBPS403RecoveryIntervalMinutesKey] = 360
	require.False(t, a.ExcelBPS403RecoveryDue(now))
	require.True(t, a.ExcelBPS403RecoveryDue(now.Add(330*time.Minute)))
}

func TestExcelBPS403RecoveryIntervalValidation(t *testing.T) {
	for _, raw := range []any{nil, 0, -1, 1.5, 10081, "30", true, math.NaN(), math.Inf(1), json.Number("1.5")} {
		t.Run(fmt.Sprintf("invalid_%v", raw), func(t *testing.T) {
			extra := map[string]any{ExcelBPS403RecoveryIntervalMinutesKey: raw}
			require.Error(t, validateExcelBPS403RecoveryExtra(extra))
			settings, err := normalizeBulkOpenAISettings(&BulkUpdateAccountsInput{Extra: extra})
			require.True(t, settings.bps)
			require.Error(t, err)
			a := recoveryTestAccount(time.Now())
			a.Extra = extra
			require.Error(t, (&adminServiceImpl{}).validateExcelBPS403GroupSettings(t.Context(), a))
			require.Equal(t, time.Hour, a.ExcelBPS403RecoveryInterval())
		})
	}
	for _, raw := range []any{1, 30, 360, 10080, float64(360), int64(30), json.Number("360")} {
		extra := map[string]any{ExcelBPS403RecoveryIntervalMinutesKey: raw}
		require.NoError(t, validateExcelBPS403RecoveryExtra(extra))
		settings, err := normalizeBulkOpenAISettings(&BulkUpdateAccountsInput{Extra: extra})
		require.NoError(t, err)
		require.True(t, settings.bps)
		a := recoveryTestAccount(time.Now())
		a.Extra = extra
		require.NoError(t, (&adminServiceImpl{}).validateExcelBPS403GroupSettings(t.Context(), a))
	}
	require.NoError(t, validateExcelBPS403RecoveryExtra(nil))
	require.Equal(t, time.Hour, (&Account{}).ExcelBPS403RecoveryInterval())
	require.Equal(t, time.Hour, (*Account)(nil).ExcelBPS403RecoveryInterval())
}

func TestExcelBPSAutomaticPolicyCountsNewSamplesAndPreservesManualChanges(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour)
	policy := &ExcelBPSAutoPolicy{Enabled: true, AutoRestore: true, BPS: &QualityBPSPolicy{FailureThreshold: 2, PassThreshold: 2, AllModels: true}}
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"plan_type": "pro"}, Extra: map[string]any{ExcelBPSAutoPolicyKey: policy, "unrelated": "preserve"}}
	state, patch, err := EvaluateExcelBPSAutoPolicy(a, ExcelBPSAutoState{}, false, now)
	require.NoError(t, err)
	require.Empty(t, patch)
	rows := []map[string]any{}
	observe := func(verdict string, seconds int) {
		rows = append(rows, map[string]any{"verdict": verdict, "at": now.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339Nano)})
		a.Extra[OpenAIAstraPolicyExtraKey] = map[string]any{"model": "gpt-6-astra", "recent_samples": rows}
	}
	observe("degraded", 1)
	state, patch, err = EvaluateExcelBPSAutoPolicy(a, state, false, now.Add(2*time.Second))
	require.NoError(t, err)
	require.Empty(t, patch)
	require.Equal(t, 1, state.FailureStreak)
	state, patch, err = EvaluateExcelBPSAutoPolicy(a, state, false, now.Add(3*time.Second))
	require.NoError(t, err)
	require.Empty(t, patch)
	require.Equal(t, 1, state.FailureStreak, "same sample is not counted twice")
	observe("unknown", 4)
	observe("degraded", 5)
	state, patch, err = EvaluateExcelBPSAutoPolicy(a, state, false, now.Add(6*time.Second))
	require.NoError(t, err)
	require.True(t, state.Managed)
	require.Equal(t, true, patch[ExcelBPSAuthorizationPendingKey])
	for k, v := range patch {
		if v == nil {
			delete(a.Extra, k)
		} else {
			a.Extra[k] = v
		}
	}
	require.Equal(t, "preserve", a.Extra["unrelated"])
	observe("healthy", 7)
	observe("healthy", 8)
	conflict := *a
	conflict.Extra = cloneExtraJSON(a.Extra)
	conflict.Extra["openai_excel_bps_cache_creation_as_input"] = true
	_, patch, err = EvaluateExcelBPSAutoPolicy(&conflict, state, true, now.Add(9*time.Second))
	require.NoError(t, err)
	require.Empty(t, patch, "manual changes cannot be overwritten")
	conflict.Extra = cloneExtraJSON(a.Extra)
	conflict.Extra[ExcelBPS403DisabledAtKey] = now.Format(time.RFC3339Nano)
	_, patch, err = EvaluateExcelBPSAutoPolicy(&conflict, state, true, now.Add(9*time.Second))
	require.NoError(t, err)
	require.Empty(t, patch, "403 disable cannot be reversed by a quality rule")
	state, patch, err = EvaluateExcelBPSAutoPolicy(a, state, true, now.Add(9*time.Second))
	require.NoError(t, err)
	require.False(t, state.Managed)
	require.Contains(t, patch, "openai_excel_bps")
	require.Nil(t, patch["openai_excel_bps"])
	require.NotContains(t, patch, "unrelated")
	require.NotContains(t, patch, OpenAIAstraPolicyExtraKey)
}
func TestExcelBPSAutomaticPolicyValidationAndUsage(t *testing.T) {
	policy := &QualityBPSPolicy{FailureThreshold: 2, UsagePercent: 80, RequireAll: true, PassThreshold: 2, AllModels: true}
	require.NoError(t, validateQualityBPSPolicy(policy))
	require.Empty(t, QualityBPSTrigger(policy, 2, 0, false))
	require.Empty(t, QualityBPSTrigger(policy, 1, 90, true))
	require.Equal(t, "degraded", QualityBPSTrigger(policy, 2, 90, true))
	policy.RequireAll = false
	require.Equal(t, "usage", QualityBPSTrigger(policy, 0, 90, true))
	policy.HoldOnUsage = true
	require.True(t, QualityBPSHoldForUsage(policy, 90, true))
	policy.UsagePercent = math.NaN()
	require.Error(t, validateQualityBPSPolicy(policy))
	require.NoError(t, validateExcelBPSDefaults(DefaultExcelBPSDefaults()))
	bad := DefaultExcelBPSDefaults()
	bad.AutoDisableOn403 = false
	bad.AutoRecoverOn403 = true
	require.Error(t, validateExcelBPSDefaults(bad))
	_, err := ParseExcelBPSAutoPolicy(map[string]any{"enabled": true, "bps": map[string]any{"failure_threshold": 0, "usage_percent": 0}})
	require.Error(t, err)
}

func TestExcelBPSUpstreamQualityBPSExtraMatchesAccountOptions(t *testing.T) {
	extra := QualityBPSExtra(&QualityBPSPolicy{Models: []string{"gpt-6-astra"}, OmitUnsupportedTools: true, IgnoreEncryptedContent: true})
	require.Equal(t, true, extra["openai_excel_bps"])
	require.Equal(t, []string{"gpt-6-astra"}, extra["openai_excel_bps_models"])
	require.Equal(t, true, extra[ExcelBPSOmitUnsupportedToolsKey])
	require.NotContains(t, extra, "openai_excel_bps_ignore_images")
	require.Equal(t, true, extra[ExcelBPSIgnoreEncryptedContentKey])
	require.Equal(t, false, extra["openai_excel_bps_mihomo"])
	require.Nil(t, extra[ExcelBPS403TargetGroupIDKey])
	for _, key := range QualityBPSManagedKeys {
		require.Contains(t, extra, key, "every managed key is written or removed")
	}
	require.Len(t, extra, len(QualityBPSManagedKeys))

	extra = QualityBPSExtra(&QualityBPSPolicy{AllModels: true, AutoMoveOn403: true, TargetGroupID: 5, SessionProxy: true, ProxySource: ExcelBPSProxySourceIPPool})
	require.Nil(t, extra["openai_excel_bps_models"], "all models = no model scope key")
	require.Equal(t, int64(5), extra[ExcelBPS403TargetGroupIDKey])
	require.Equal(t, ExcelBPSProxySourceIPPool, extra[ExcelBPSProxySourceKey])

	// 写入后的账号按规则的模型范围走 BPS。
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	for key, value := range QualityBPSExtra(&QualityBPSPolicy{Models: []string{"gpt-6-astra"}}) {
		if value != nil {
			account.Extra[key] = value
		}
	}
	require.True(t, account.IsExcelBPSEnabledForModel("gpt-6-astra"))
	require.False(t, account.IsExcelBPSEnabledForModel("gpt-5.6-sol"))
}

func TestExcelBPSUpstreamQualityBPSTriggerModes(t *testing.T) {
	either := &QualityBPSPolicy{FailureThreshold: 3, UsagePercent: 80}
	require.Equal(t, "", QualityBPSTrigger(either, 2, 79, true))
	require.Equal(t, "degraded", QualityBPSTrigger(either, 3, 0, false))
	require.Equal(t, "usage", QualityBPSTrigger(either, 0, 80, true))
	require.Equal(t, "", QualityBPSTrigger(either, 0, 99, false), "no usage snapshot never triggers")

	all := &QualityBPSPolicy{FailureThreshold: 3, UsagePercent: 80, RequireAll: true}
	require.Equal(t, "", QualityBPSTrigger(all, 3, 50, true))
	require.Equal(t, "", QualityBPSTrigger(all, 1, 90, true))
	require.Equal(t, "degraded", QualityBPSTrigger(all, 3, 90, true))

	countOnly := &QualityBPSPolicy{FailureThreshold: 1, RequireAll: true}
	require.Equal(t, "degraded", QualityBPSTrigger(countOnly, 1, 0, false), "require_all with one trigger behaves like that trigger")
	require.Equal(t, "", QualityBPSTrigger(nil, 10, 100, true))

	require.False(t, QualityBPSHoldForUsage(either, 85, true), "holding for usage is opt-in")
	either.HoldOnUsage, all.HoldOnUsage = true, true
	require.True(t, QualityBPSHoldForUsage(either, 85, true))
	require.False(t, QualityBPSHoldForUsage(either, 20, true))
	require.False(t, QualityBPSHoldForUsage(either, 99, false), "no usage snapshot never holds")
	require.False(t, QualityBPSHoldForUsage(all, 85, true), "require_all rules restore once the probe passes")
	require.False(t, QualityBPSHoldForUsage(&QualityBPSPolicy{FailureThreshold: 2, HoldOnUsage: true}, 100, true))

	require.Equal(t, 1, QualityBPSPassThreshold(nil), "a rule that no longer enables BPS restores after one pass")
	require.Equal(t, 1, QualityBPSPassThreshold(&QualityBPSPolicy{}))
	require.Equal(t, 3, QualityBPSPassThreshold(&QualityBPSPolicy{PassThreshold: 3}))
}

func TestExcelBPSUpstreamQualityUsagePercentIgnoresResetWindows(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	used, ok := QualityUsagePercent(map[string]any{}, now)
	require.False(t, ok)
	require.Zero(t, used)

	used, ok = QualityUsagePercent(map[string]any{
		"codex_5h_used_percent": 91.5, "codex_5h_reset_at": now.Add(-time.Minute).Format(time.RFC3339),
		"codex_7d_used_percent": 40.0, "codex_7d_reset_at": now.Add(time.Hour).Format(time.RFC3339),
	}, now)
	require.True(t, ok)
	require.Equal(t, 40.0, used, "an expired 5h window counts as 0")

	used, _ = QualityUsagePercent(map[string]any{"codex_5h_used_percent": 91.5, "codex_7d_used_percent": 40.0}, now)
	require.Equal(t, 91.5, used)
}
