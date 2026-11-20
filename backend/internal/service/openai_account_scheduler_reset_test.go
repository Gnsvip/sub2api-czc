package service

import (
	"context"
	"strconv"
	"sync"
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

func TestOpenAIAccountExpiryWindowAndUndatedOrder(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name   string
		offset time.Duration
		want   bool
	}{
		{"one hour", time.Hour, true}, {"24 hours inclusive", 24 * time.Hour, true},
		{"more than 24 hours", 24*time.Hour + time.Second, false}, {"already expired", -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expires := now.Add(tc.offset)
			a := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, ExpiresAt: &expires}
			require.Equal(t, tc.want, openAIAccountExpirySoon(a, now))
		})
	}
	soon := now.Add(time.Hour)
	pool := []openAIAccountCandidateScore{
		{account: &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 50, Priority: 2, ExpiresAt: &soon}, loadInfo: &AccountLoadInfo{}, loadKnown: true},
		{account: &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 50, Priority: 1}, loadInfo: &AccountLoadInfo{}, loadKnown: true},
	}
	order := buildOpenAIExpirySelectionOrder(pool, now)
	require.Equal(t, int64(2), order[0].account.ID, "expiry cannot cross an explicit higher priority")
	for _, scenario := range []string{"undated", "unknown load", "queued", "unlimited", "api key"} {
		t.Run(scenario, func(t *testing.T) {
			a := *pool[0].account
			candidate := pool[0]
			candidate.account = &a
			switch scenario {
			case "undated":
				a.ExpiresAt = nil
			case "unknown load":
				candidate.loadKnown = false
			case "queued":
				candidate.loadInfo = &AccountLoadInfo{WaitingCount: 1}
			case "unlimited":
				a.Concurrency = 0
			case "api key":
				a.Type = AccountTypeAPIKey
			}
			original := []openAIAccountCandidateScore{candidate, pool[1]}
			require.Equal(t, original, buildOpenAIExpirySelectionOrder(original, now), "unmanaged pools retain the exact original order")
		})
	}
	for _, limit := range []int{1, 2, 5, 50} {
		a := &Account{Concurrency: limit}
		require.Equal(t, max(1, limit*4/5), openAIExpirySoftLimit(a))
		a.ProxyIDs = []int64{11, 22, 33}
		require.Equal(t, max(1, limit*4/5)*3, openAIExpiryTotalSoftLimit(a))
	}
}

func TestOpenAIAccountExpirySoftCapAndCrossGroupAdmission(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, scenario := range []string{"parallel stale loads", "soft cap spill", "hard cap fallback", "sticky above soft cap", "stale high load"} {
			t.Run(strconv.FormatBool(advanced)+"/"+scenario, func(t *testing.T) {
				soon, later := time.Now().Add(time.Hour), time.Now().Add(4*time.Hour)
				accounts := []Account{
					{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 50, Priority: 1, GroupIDs: []int64{42, 84}, ExpiresAt: &soon, Extra: astraHealthyTestExtra(nil)},
					{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 50, Priority: 1, GroupIDs: []int64{42, 84}, ExpiresAt: &later, Extra: astraHealthyTestExtra(nil)},
				}
				counts := map[int64]int{1: 13, 2: 0}
				if scenario != "parallel stale loads" {
					counts[1] = 40
				}
				if scenario == "hard cap fallback" {
					counts[2] = 50
				}
				if scenario == "sticky above soft cap" {
					counts[2] = 49
				}
				if scenario == "stale high load" {
					counts[1] = 39
				}
				var mu sync.Mutex
				cache := schedulerTestConcurrencyCache{
					// Every request sees the same stale load; the reservation
					// limit must enforce 40 atomically across both groups.
					loadMap: map[int64]*AccountLoadInfo{1: {AccountID: 1, CurrentConcurrency: 13, LoadRate: 26}, 2: {AccountID: 2}},
					acquireHook: func(id int64, limit int) (bool, error) {
						mu.Lock()
						defer mu.Unlock()
						if counts[id] >= limit {
							return false, nil
						}
						counts[id]++
						return true, nil
					},
				}
				if scenario == "stale high load" {
					cache.loadMap[1] = &AccountLoadInfo{AccountID: 1, CurrentConcurrency: 40, LoadRate: 80}
				}
				cfg := openAIResetTestScheduler(0).service.cfg
				cfg.Gateway.Scheduling.LoadBatchEnabled = true
				cfg.Gateway.OpenAIWS.LBTopK = 1
				svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: cfg,
					rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(strconv.FormatBool(advanced), "false"),
					concurrencyService: NewConcurrencyService(cache)}
				session := ""
				if scenario == "sticky above soft cap" {
					session = "existing"
					svc.cache = &schedulerTestGatewayCache{sessionBindings: map[string]int64{"openai:existing": 2}}
				}
				n := 1
				if scenario == "parallel stale loads" {
					n = 40
				}
				selections := make([]*AccountSelectionResult, n)
				decisions := make([]OpenAIAccountScheduleDecision, n)
				errs := make([]error, n)
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						group := int64(42)
						if i%2 != 0 {
							group = 84
						}
						selections[i], decisions[i], errs[i] = svc.SelectAccountWithScheduler(context.Background(), &group, "", session, "gpt-6.2-sol", nil, OpenAIUpstreamTransportAny, false)
					}(i)
				}
				wg.Wait()
				for i, selection := range selections {
					require.NoError(t, errs[i])
					require.NotNil(t, selection)
					require.True(t, selection.Acquired)
					require.Nil(t, selection.WaitPlan)
					selection.ReleaseFunc()
				}
				switch scenario {
				case "parallel stale loads":
					require.Equal(t, 40, counts[1], "new traffic fills the earliest expiry to 80 percent")
					require.Equal(t, 13, counts[2], "excess new traffic immediately spills to the other account")
				case "soft cap spill":
					require.Equal(t, int64(2), selections[0].Account.ID)
					require.Equal(t, int64(1), decisions[0].ExpiryCandidateID)
					require.Equal(t, "soft_limit", decisions[0].ExpiryReason)
				case "stale high load":
					require.Equal(t, int64(1), selections[0].Account.ID, "atomic reservation, not a stale 80-percent snapshot, decides spillover")
					require.Equal(t, "preferred", decisions[0].ExpiryMode)
					require.Equal(t, 40, counts[1])
				case "sticky above soft cap":
					require.Equal(t, int64(2), selections[0].Account.ID)
					require.True(t, decisions[0].StickySessionHit)
					require.Empty(t, decisions[0].ExpiryMode)
					require.Equal(t, 50, counts[2], "sticky continuation retains its original hard cap")
				case "hard cap fallback":
					require.Equal(t, int64(1), selections[0].Account.ID)
					require.Equal(t, "overflow", decisions[0].ExpiryMode)
					require.Equal(t, 41, counts[1], "soft cap must not waste remaining hard capacity when alternatives are full")
				}
				require.Equal(t, 50, accounts[0].Concurrency, "admission must not mutate the shared hard limit")
			})
		}
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
