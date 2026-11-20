package service

import "time"

// Business expiry is independent of the OAuth token's short-lived expires_at.
// A bounded bonus favors using remaining time, without changing eligibility,
// retry/cooldown policies, concurrency slots, or existing conversation bindings.
func openAIAccountExpiryBonus(account *Account, load *AccountLoadInfo, errorRate float64, now time.Time) float64 {
	if account == nil || !account.IsOpenAIOAuth() || account.ExpiresAt == nil || load == nil || load.WaitingCount > 0 {
		return 0
	}
	// Reuse the scheduler's existing load scale; do not add a new load limit.
	spare := 1 - clamp01(float64(load.LoadRate)/100)
	remaining := account.ExpiresAt.Sub(now).Hours()
	if remaining < 0 {
		remaining = 0 // Expired but usable accounts remain eligible by existing policy.
	}
	urgency := (7 * 24.0) / (7*24.0 + remaining)
	return 0.5 * urgency * spare * (1 - clamp01(errorRate))
}

// Legacy HTTP scheduling keeps its priority/capability partitions. Only pools
// with a known, dated account receive a weighted order; undated pools retain
// their existing least-load order. Only break ties at the same priority and
// load; the shared sampler prevents a fixed winner.
func orderOpenAIExpiryCandidates(available []accountWithLoad, loads map[int64]*AccountLoadInfo, req OpenAIAccountScheduleRequest, now time.Time) {
	for start := 0; start < len(available); {
		end := start + 1
		for end < len(available) && available[end].account.Priority == available[start].account.Priority && available[end].loadInfo.LoadRate == available[start].loadInfo.LoadRate {
			end++
		}
		pool := make([]openAIAccountCandidateScore, 0, end-start)
		hasBonus := false
		for _, item := range available[start:end] {
			bonus := openAIAccountExpiryBonus(item.account, loads[item.account.ID], 0, now)
			hasBonus = hasBonus || bonus > 0
			pool = append(pool, openAIAccountCandidateScore{
				account: item.account, loadInfo: item.loadInfo,
				score: 1 - clamp01(float64(item.loadInfo.LoadRate)/100) + bonus,
			})
		}
		if hasBonus {
			for i, candidate := range buildOpenAIWeightedSelectionOrder(pool, req) {
				available[start+i] = accountWithLoad{account: candidate.account, loadInfo: candidate.loadInfo}
			}
		}
		start = end
	}
}
