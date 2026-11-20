package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// Reproduces the production complaint: least-load/random weights can select a
// later expiry even though the earliest account has ample concurrency headroom.
func TestOpenAIAccountExpiryPrioritizesNewAssignments(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(strconv.FormatBool(advanced), func(t *testing.T) {
			soon, later := time.Now().Add(time.Hour), time.Now().Add(4*time.Hour)
			groupID := int64(42)
			accounts := []Account{
				{ID: 12376, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 50, Priority: 1, GroupIDs: []int64{groupID}, ExpiresAt: &soon, Extra: astraHealthyTestExtra(nil)},
				{ID: 12377, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 50, Priority: 1, GroupIDs: []int64{groupID}, ExpiresAt: &later, Extra: astraHealthyTestExtra(nil)},
			}
			cfg := openAIResetTestScheduler(0).service.cfg
			cfg.Gateway.Scheduling.LoadBatchEnabled = true
			cfg.Gateway.OpenAIWS.LBTopK = 1
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: cfg,
				rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(strconv.FormatBool(advanced), "false"),
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{
					12376: {AccountID: 12376, CurrentConcurrency: 13, LoadRate: 26},
					12377: {AccountID: 12377},
				}}),
			}
			selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-6.2-sol", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			t.Cleanup(selection.ReleaseFunc)
			require.True(t, selection.Acquired)
			require.Equal(t, int64(12376), selection.Account.ID, "new allocation must prefer the earliest expiry below its soft cap")
		})
	}
}

func TestOpenAIAccountExpiryPreferencePreservesLoadAndDistribution(t *testing.T) {
	now := time.Now()
	soon, later := now.Add(time.Hour), now.Add(30*24*time.Hour)
	accounts := []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ExpiresAt: &soon},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, ExpiresAt: &later},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	}
	loads := map[int64]*AccountLoadInfo{1: {AccountID: 1}, 2: {AccountID: 2}, 3: {AccountID: 3}}
	sched := openAIResetTestScheduler(0)
	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, accounts, loads)
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[1], scores[2])
	require.Greater(t, scores[2], scores[3])
	counts := map[int64]int{}
	for i := range 4000 {
		order := buildOpenAIWeightedSelectionOrder(plan.candidates, OpenAIAccountScheduleRequest{SessionHash: strconv.Itoa(i)})
		counts[order[0].account.ID]++
	}
	require.Greater(t, counts[1], counts[2])
	require.Greater(t, counts[1], counts[3])
	require.Less(t, counts[1], 2800, "expiry preference must not concentrate all new sessions on one account")
	for _, load := range []*AccountLoadInfo{{LoadRate: 100}, {WaitingCount: 1}, nil} {
		require.Zero(t, openAIAccountExpiryBonus(accounts[0], load, 0, now))
	}
	require.Greater(t, openAIAccountExpiryBonus(accounts[0], loads[1], 0, now), openAIAccountExpiryBonus(accounts[0], &AccountLoadInfo{LoadRate: 50}, 0, now))
	require.Zero(t, openAIAccountExpiryBonus(accounts[0], loads[1], 1, now))
	loads[1].LoadRate = 60
	stressed := openAIPlanScores(sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, accounts, loads))
	require.Less(t, stressed[1], stressed[3], "an idle undated account outranks a busy expiring account")
	for id, snapshot := range buildOpenAIAccountSchedulerScoreSnapshot(accounts, loads, sched.service.openAIWSSchedulerWeights(), false, nil) {
		require.InDelta(t, stressed[id], snapshot.BaseScore, 0.001, "admin score must include the same expiry preference")
	}
	sched.service.cfg.Gateway.OpenAIWS.LBTopK = 1
	loads[1].LoadRate = 0
	plan = sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, accounts, loads)
	require.Len(t, plan.selectionOrder, 3, "a full preferred account must retain other eligible slot candidates")
}

func TestOpenAIAccountExpiryLegacyKeepsPriorityLoadAndUndatedOrder(t *testing.T) {
	now := time.Now()
	soon := now.Add(time.Hour)
	loads := map[int64]*AccountLoadInfo{1: {AccountID: 1}, 2: {AccountID: 2}, 3: {AccountID: 3, LoadRate: 10}}
	makePool := func() []accountWithLoad {
		return []accountWithLoad{
			{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 1, ExpiresAt: &soon}, loadInfo: loads[1]},
			{account: &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 1}, loadInfo: loads[2]},
			{account: &Account{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Priority: 1, ExpiresAt: &soon}, loadInfo: loads[3]},
		}
	}
	count := map[int64]int{}
	for i := range 2000 {
		pool := makePool()
		orderOpenAIExpiryCandidates(pool, loads, OpenAIAccountScheduleRequest{SessionHash: strconv.Itoa(i)}, now)
		count[pool[0].account.ID]++
		require.Equal(t, int64(3), pool[2].account.ID, "date must not override the existing least-load order")
	}
	require.Greater(t, count[1], count[2])
	require.Greater(t, count[2], 500, "undated accounts must still receive traffic")
	pool := makePool()
	pool[0].account.ExpiresAt = nil
	pool[2].account.ExpiresAt = nil
	before := append([]accountWithLoad(nil), pool...)
	orderOpenAIExpiryCandidates(pool, loads, OpenAIAccountScheduleRequest{SessionHash: "unchanged"}, now)
	require.Equal(t, before, pool)
	pool = makePool()
	pool[0].account.Priority = 0
	for i := range 30 {
		orderOpenAIExpiryCandidates(pool, loads, OpenAIAccountScheduleRequest{SessionHash: strconv.Itoa(i)}, now)
		require.Equal(t, int64(1), pool[0].account.ID)
	}
}

func TestOpenAIAccountExpiryUsesExistingSchedulingGuards(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, scenario := range []string{"sticky", "weighted sticky", "cooldown", "admin pause", "slot full", "expired but usable", "model cooldown"} {
			t.Run(strconv.FormatBool(advanced)+"/"+scenario, func(t *testing.T) {
				now := time.Now()
				soon, cooldown := now.Add(time.Hour), now.Add(10*time.Minute)
				groupID := int64(10)
				accounts := []Account{
					{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}, ExpiresAt: &soon, Extra: astraHealthyTestExtra(nil)},
					{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID}, Extra: astraHealthyTestExtra(nil)},
				}
				cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
				acquired := []int64{}
				concurrency := schedulerTestConcurrencyCache{acquiredIDs: &acquired}
				var excluded map[int64]struct{}
				want := int64(2)
				switch scenario {
				case "sticky", "weighted sticky":
					cache.sessionBindings["openai:expiry-session"] = 2
				case "cooldown":
					accounts[0].RateLimitResetAt = &cooldown
				case "admin pause":
					accounts[0].Schedulable = false
				case "slot full":
					concurrency.acquireResults = map[int64]bool{1: false, 2: true}
				case "expired but usable":
					past := now.Add(-time.Hour)
					accounts[0].ExpiresAt = &past
					excluded = map[int64]struct{}{2: {}}
					want = 1
				case "model cooldown":
					accounts[0].Extra[modelRateLimitsKey] = map[string]any{
						"gpt-5.1": map[string]any{"rate_limit_reset_at": cooldown.Format(time.RFC3339)},
					}
				}
				cfg := openAIResetTestScheduler(0).service.cfg
				cfg.Gateway.Scheduling.LoadBatchEnabled = true
				cfg.Gateway.OpenAIWS.LBTopK = 1
				cfg.Gateway.OpenAIWS.SchedulerScoreWeights.SessionSticky = 0.1
				svc := &OpenAIGatewayService{
					accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: cfg,
					rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(strconv.FormatBool(advanced), strconv.FormatBool(scenario == "weighted sticky")),
					concurrencyService: NewConcurrencyService(concurrency),
				}
				selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "expiry-session", "gpt-5.1", excluded, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.True(t, selection.Acquired)
				require.Equal(t, want, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
				if scenario == "sticky" || scenario == "weighted sticky" {
					require.Equal(t, int64(2), cache.sessionBindings["openai:expiry-session"])
					require.NotContains(t, acquired, int64(1), "a nearer expiry must not displace a usable bound account")
				}
				if scenario == "model cooldown" {
					selection, _, err = svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "other-model", "gpt-5.2", map[int64]struct{}{2: {}}, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.Equal(t, int64(1), selection.Account.ID, "a model cooldown must not disable the whole account")
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				}
			})
		}
	}
}

func openAIResetTestScheduler(reset float64) *defaultOpenAIAccountScheduler {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights = config.GatewayOpenAIWSSchedulerScoreWeights{
		Priority:      1.0,
		Load:          1.0,
		Queue:         0.7,
		ErrorRate:     0.8,
		TTFT:          0.5,
		Reset:         reset,
		QuotaHeadroom: 0,
	}
	return &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: cfg}}
}

func openAIQuotaHeadroomTestScheduler(quotaHeadroom float64) *defaultOpenAIAccountScheduler {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights = config.GatewayOpenAIWSSchedulerScoreWeights{
		QuotaHeadroom: quotaHeadroom,
	}
	return &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: cfg}}
}

func openAIPlanScores(plan openAIAccountLoadPlan) map[int64]float64 {
	scores := make(map[int64]float64, len(plan.candidates))
	for _, c := range plan.candidates {
		scores[c.account.ID] = c.score
	}
	return scores
}

func openAIAccountWith7dReset(id int64, resetAt time.Time) *Account {
	return &Account{
		ID:       id,
		Priority: 0,
		Extra: map[string]any{
			"codex_7d_reset_at":      resetAt.Format(time.RFC3339),
			"codex_usage_updated_at": resetAt.Add(-time.Hour).Format(time.RFC3339),
		},
	}
}

// Reset 权重 > 0 时，7 天额度窗口最早重置的账号应获得更高分。
func TestBuildOpenAIAccountLoadPlan_ResetWeightPrefersSoonestReset(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(20 * time.Hour)
	filtered := []*Account{
		openAIAccountWith7dReset(1, later),
		openAIAccountWith7dReset(2, soon),
	}
	sched := openAIResetTestScheduler(5.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "7 天额度最早重置的账号（ID=2）得分更高")
}

// Reset 权重为 0 时，窗口重置时间不应影响打分。
func TestBuildOpenAIAccountLoadPlan_ResetWeightZeroNoEffect(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(20 * time.Hour)
	filtered := []*Account{
		openAIAccountWith7dReset(1, later),
		openAIAccountWith7dReset(2, soon),
	}
	sched := openAIResetTestScheduler(0.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Equal(t, scores[1], scores[2], "Reset 权重为 0 时两账号得分相同")
}

// 无 7 天额度窗口的账号 reset 因子为 0，应低于拥有未来 7d 窗口的账号。
func TestBuildOpenAIAccountLoadPlan_ResetWeightIgnoresNilWindow(t *testing.T) {
	now := time.Now()
	soon := now.Add(2 * time.Hour)
	filtered := []*Account{
		{ID: 1, Priority: 0},
		openAIAccountWith7dReset(2, soon),
	}
	sched := openAIResetTestScheduler(5.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "拥有 7 天额度窗口的账号得分高于无窗口账号")
}

func TestOpenAIQuotaHeadroomFactor_PrimaryUsedPercent(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_primary_used_percent": 20.0,
			"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.8, openAIQuotaHeadroomFactor(account, now), 0.0001)
}

func TestOpenAIQuotaHeadroomFactor_PrimaryMissingIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, openAIQuotaHeadroomFactor(account, now))
}

func TestOpenAIQuotaHeadroomFactor_PrimaryResetExpiredIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_primary_used_percent": 20.0,
			"codex_primary_reset_at":     now.Add(-time.Minute).Format(time.RFC3339),
			"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, openAIQuotaHeadroomFactor(account, now))
}

func TestOpenAIQuotaHeadroomFactor_SecondaryLowHeadroomDiscountsPrimary(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_primary_used_percent":   20.0,
			"codex_primary_reset_at":       now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_secondary_used_percent": 95.0,
			"codex_secondary_reset_at":     now.Add(time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at":       now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.4, openAIQuotaHeadroomFactor(account, now), 0.0001)
}

func TestBuildOpenAIAccountLoadPlan_QuotaHeadroomPrefersHigher7dRemaining(t *testing.T) {
	now := time.Now()
	filtered := []*Account{
		{
			ID:       1,
			Priority: 0,
			Extra: map[string]any{
				"codex_primary_used_percent": 80.0,
				"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:       2,
			Priority: 0,
			Extra: map[string]any{
				"codex_primary_used_percent": 20.0,
				"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	sched := openAIQuotaHeadroomTestScheduler(1.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "7d 剩余额度更高的账号得分应更高")
}

func TestBuildOpenAIAccountLoadPlan_QuotaHeadroomZeroNoEffect(t *testing.T) {
	now := time.Now()
	filtered := []*Account{
		{
			ID:       1,
			Priority: 0,
			Extra: map[string]any{
				"codex_primary_used_percent": 80.0,
				"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:       2,
			Priority: 0,
			Extra: map[string]any{
				"codex_primary_used_percent": 20.0,
				"codex_primary_reset_at":     now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at":     now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	sched := openAIResetTestScheduler(0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Equal(t, scores[1], scores[2], "quota_headroom 权重为 0 时不应影响打分")
}
