package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

const ExcelBPSAutoPolicyKey = "openai_excel_bps_auto_policy"

type ExcelBPSAutoPolicy struct {
	Enabled     bool              `json:"enabled"`
	AutoRestore bool              `json:"auto_restore"`
	BPS         *QualityBPSPolicy `json:"bps"`
}
type ExcelBPSAutoState struct {
	ConfigHash    string                     `json:"config_hash"`
	LastSample    time.Time                  `json:"last_sample"`
	FailureStreak int                        `json:"failure_streak"`
	PassStreak    int                        `json:"pass_streak"`
	Managed       bool                       `json:"managed"`
	Previous      map[string]json.RawMessage `json:"previous,omitempty"`
	Applied       map[string]json.RawMessage `json:"applied,omitempty"`
	Status        string                     `json:"status"`
}
type ExcelBPSAutoRepository interface {
	ApplyExcelBPSAutoPolicy(context.Context, int64, time.Time) error
}

func ParseExcelBPSAutoPolicy(raw any) (*ExcelBPSAutoPolicy, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var p ExcelBPSAutoPolicy
	if err = json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("invalid automatic BPS policy")
	}
	if !p.Enabled {
		return &p, nil
	}
	if err = validateQualityBPSPolicy(p.BPS); err != nil {
		return nil, err
	}
	return &p, nil
}
func bpsManagedSnapshot(extra map[string]any) map[string]json.RawMessage {
	result := map[string]json.RawMessage{}
	for _, key := range QualityBPSManagedKeys {
		if value, ok := extra[key]; ok && value != nil {
			result[key], _ = json.Marshal(value)
		}
	}
	return result
}
func bpsManagedEqual(a, b map[string]json.RawMessage) bool {
	for _, key := range QualityBPSManagedKeys {
		left, right := a[key], b[key]
		fallback := json.RawMessage("false")
		switch key {
		case ExcelBPSProxySourceKey:
			fallback = json.RawMessage(`"mihomo"`)
		case ExcelBPS403RecoveryIntervalMinutesKey:
			fallback = json.RawMessage(`60`)
		case "openai_excel_bps_models", ExcelBPS403TargetGroupIDKey:
			fallback = json.RawMessage(`null`)
		}
		if len(left) == 0 {
			left = fallback
		}
		if len(right) == 0 {
			right = fallback
		}
		var x, y any
		if json.Unmarshal(left, &x) != nil || json.Unmarshal(right, &y) != nil || !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

// Pure decision logic adapted from upstream quality BPS. Native Astra samples
// are the only quality input; pelican grades and Tibo group policy are untouched.
// A saved configuration starts counting new observations, never old history.
func EvaluateExcelBPSAutoPolicy(account *Account, state ExcelBPSAutoState, grantReady bool, now time.Time) (ExcelBPSAutoState, map[string]any, error) {
	policy, err := ParseExcelBPSAutoPolicy(account.Extra[ExcelBPSAutoPolicyKey])
	if err != nil {
		return state, nil, err
	}
	if policy == nil || !policy.Enabled {
		return state, nil, nil
	}
	if !QualityBPSEligible(account) || !account.IsActive() {
		return state, nil, nil
	}
	encoded, _ := json.Marshal(policy)
	hash := sha256.Sum256(encoded)
	configHash := hex.EncodeToString(hash[:])
	if state.ConfigHash != configHash {
		state.ConfigHash = configHash
		state.LastSample = now
		state.FailureStreak = 0
		state.PassStreak = 0
	}
	freshHealthy := false
	for _, sample := range validOpenAIAstraSamples(account.Extra[OpenAIAstraPolicyExtraKey], now) {
		if !sample.At.After(state.LastSample) {
			continue
		}
		state.LastSample = sample.At
		switch sample.Verdict {
		case openAITiboDegraded:
			state.FailureStreak++
			state.PassStreak = 0
			freshHealthy = false
		case openAITiboHealthy:
			state.PassStreak++
			state.FailureStreak = 0
			freshHealthy = true
		}
	}
	if state.FailureStreak > 100 {
		state.FailureStreak = 100
	}
	if state.PassStreak > 100 {
		state.PassStreak = 100
	}
	usage, hasUsage := QualityUsagePercent(account.Extra, now)
	if state.Managed {
		if !bpsManagedEqual(bpsManagedSnapshot(account.Extra), state.Applied) || account.Extra[ExcelBPS403DisabledAtKey] != nil {
			state.Status = "manual_or_403_change"
			return state, nil, nil
		}
		if !policy.AutoRestore || !freshHealthy || state.PassStreak < QualityBPSPassThreshold(policy.BPS) || QualityBPSHoldForUsage(policy.BPS, usage, hasUsage) {
			state.Status = "managed"
			return state, nil, nil
		}
		patch := map[string]any{ExcelBPSAuthorizationPendingKey: nil}
		for _, key := range QualityBPSManagedKeys {
			patch[key] = nil
			if raw, ok := state.Previous[key]; ok {
				var value any
				if err = json.Unmarshal(raw, &value); err != nil {
					return state, nil, err
				}
				patch[key] = value
			}
		}
		state.Managed = false
		state.Previous = nil
		state.Applied = nil
		state.PassStreak = 0
		state.FailureStreak = 0
		state.Status = "restored"
		return state, patch, nil
	}
	trigger := QualityBPSTrigger(policy.BPS, state.FailureStreak, usage, hasUsage)
	if trigger == "" {
		state.Status = "observing"
		return state, nil, nil
	}
	if account.Extra[ExcelBPS403DisabledAtKey] != nil {
		state.Status = "blocked_403"
		return state, nil, nil
	}
	if account.Extra["openai_excel_bps"] == true {
		state.Status = "already_enabled"
		return state, nil, nil
	}
	patch := QualityBPSExtra(policy.BPS)
	patch[OpenAIExcelBPSSessionProxyKey] = false
	patch[ExcelBPSAuthorizationPendingKey] = nil
	if !account.IsExcelOAuth() && !grantReady {
		patch[ExcelBPSAuthorizationPendingKey] = true
	}
	state.Previous = bpsManagedSnapshot(account.Extra)
	state.Applied = bpsManagedSnapshot(patch)
	state.Managed = true
	state.PassStreak = 0
	state.Status = "enabled_" + trigger
	return state, patch, nil
}
