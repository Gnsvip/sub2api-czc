package service

import (
	"context"
	"sort"
	"time"
)

const openAIExpiryPreferenceWindow = 24 * time.Hour

// Business expiry never changes eligibility or the OAuth token's expires_at.
// Past dates remain usable according to auto_pause_on_expired, but no longer
// receive a preference that would otherwise last indefinitely.
func openAIAccountExpirySoon(account *Account, now time.Time) bool {
	return account != nil && account.IsOpenAIOAuth() && account.ExpiresAt != nil &&
		account.ExpiresAt.After(now) && !account.ExpiresAt.After(now.Add(openAIExpiryPreferenceWindow))
}

// Use the existing distributed reservation with a smaller admission limit.
// For proxy pools this is per exit, exactly like the original hard limit.
// Never mutate Account.Concurrency: sticky requests retain the full capacity.
func openAIExpirySoftLimit(account *Account) int {
	return max(1, account.Concurrency*4/5)
}

func openAIExpiryTotalSoftLimit(account *Account) int {
	return openAIExpirySoftLimit(account) * max(1, len(account.ProxyIDs))
}

func openAIExpiryManaged(candidate openAIAccountCandidateScore, now time.Time) bool {
	return openAIAccountExpirySoon(candidate.account, now) && candidate.account.Concurrency > 0 &&
		candidate.loadKnown && candidate.loadInfo != nil && candidate.loadInfo.WaitingCount == 0
}

// Called inside existing capability/route/cost partitions. Within a priority,
// try earliest expiry with an atomic soft cap, then other accounts, and only
// then the dated accounts at their original hard cap. No extra wait is added.
// Pools without an eligible expiry retain their original order exactly.
func buildOpenAIExpirySelectionOrder(pool []openAIAccountCandidateScore, now time.Time) []openAIAccountCandidateScore {
	hasExpiry := false
	for _, candidate := range pool {
		hasExpiry = hasExpiry || openAIExpiryManaged(candidate, now)
	}
	if !hasExpiry {
		return pool
	}
	ordered := append([]openAIAccountCandidateScore(nil), pool...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return openAIAccountSchedulingPriority(ordered[i].account) < openAIAccountSchedulingPriority(ordered[j].account)
	})
	result := make([]openAIAccountCandidateScore, 0, len(pool)*2)
	for start := 0; start < len(ordered); {
		end := start + 1
		for end < len(ordered) && openAIAccountSchedulingPriority(ordered[end].account) == openAIAccountSchedulingPriority(ordered[start].account) {
			end++
		}
		var expiring, regular []openAIAccountCandidateScore
		for _, candidate := range ordered[start:end] {
			if openAIExpiryManaged(candidate, now) {
				expiring = append(expiring, candidate)
			} else {
				regular = append(regular, candidate)
			}
		}
		sort.SliceStable(expiring, func(i, j int) bool {
			return expiring[i].account.ExpiresAt.Before(*expiring[j].account.ExpiresAt)
		})
		for _, candidate := range expiring {
			candidate.expiryMode = "preferred"
			result = append(result, candidate)
		}
		result = append(result, regular...)
		for _, candidate := range expiring {
			candidate.expiryMode = "overflow"
			result = append(result, candidate)
		}
		start = end
	}
	return result
}

func openAIExpiryAcquireLimit(candidate openAIAccountCandidateScore, fresh *Account) int {
	if candidate.expiryMode == "preferred" {
		return openAIExpirySoftLimit(fresh)
	}
	return fresh.Concurrency
}

// Per-selection diagnostics contain fixed reason codes and numeric IDs only.
// They travel with the existing access log; no global maps or extra log stream.
type openAIExpiryTraceKey struct{}
type openAIExpiryTrace struct {
	accountID int64
	expiresAt time.Time
	reason    string
	load      int
	softLimit int
}

func observeOpenAIExpiry(ctx context.Context, account *Account, reason string, load *AccountLoadInfo) {
	trace, ok := ctx.Value(openAIExpiryTraceKey{}).(*openAIExpiryTrace)
	if !ok || !openAIAccountExpirySoon(account, time.Now()) {
		return
	}
	if trace.accountID != 0 && trace.accountID != account.ID && !account.ExpiresAt.Before(trace.expiresAt) {
		return
	}
	if trace.accountID != account.ID {
		*trace = openAIExpiryTrace{accountID: account.ID, expiresAt: *account.ExpiresAt, reason: "policy_order"}
	}
	if reason != "" {
		trace.reason = reason
	}
	if load != nil {
		trace.load = load.CurrentConcurrency
	}
	trace.softLimit = openAIExpiryTotalSoftLimit(account)
}
