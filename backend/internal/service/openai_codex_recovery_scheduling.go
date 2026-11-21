package service

import (
	"context"
	"strings"
	"time"
)

// This timestamp identifies one verified reauthorization, across scheduler snapshots.
const CodexRecoveryGrantKey = "openai_codex_recovery_grant"
const CodexRecoverySessionLimit = 2
const CodexRecoveryWindow = 24 * time.Hour

type CodexRecoverySessionCache interface {
	StartCodexRecovery(ctx context.Context, accountID int64, at time.Time) error
	CodexRecoveryAvailable(ctx context.Context, accountID int64, grant string) (bool, error)
	ClaimCodexRecoverySession(ctx context.Context, accountID int64, grant string, binding CodexRecoveryBinding) (bool, error)
}

type CodexRecoveryBinding struct {
	GroupID              int64
	Primary, Legacy      string
	TTL, LegacyTTL       time.Duration
	BindNow, WriteLegacy bool
}

func (req OpenAIAccountScheduleRequest) newCodexSession() bool {
	return req.Platform == PlatformOpenAI && strings.TrimSpace(req.SessionHash) != "" &&
		req.StickyAccountID == 0 && req.StickyPreviousAccountID == 0 && req.GuardianParentAccountID == 0 &&
		req.PreviousResponseID == "" && !req.PreserveStickyBinding && len(req.ExcludedIDs) == 0
}

// Speculative attempts are inside the winning priority and routing tier. Keep
// the entire original order behind them so a spent grant cannot change fallback.
func preferCodexRecoverySessions(req OpenAIAccountScheduleRequest, order, pool []openAIAccountCandidateScore) []openAIAccountCandidateScore {
	if !req.newCodexSession() || len(order) == 0 || order[0].account == nil || order[0].expiryMode != "" {
		return order
	}
	var preferred []openAIAccountCandidateScore
	now := time.Now()
	for _, item := range pool {
		a := item.account
		if a == nil || a.Priority != order[0].account.Priority || !a.IsOpenAIOAuth() || !a.IsSchedulable() ||
			(item.loadInfo != nil && (item.loadInfo.WaitingCount > 0 || item.loadInfo.LoadRate >= 100)) || openAIAccountExpirySoon(a, now) {
			continue
		}
		grant := a.GetExtraString(CodexRecoveryGrantKey)
		if grant == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, grant)
		if err != nil || at.After(now) || now.Sub(at) >= CodexRecoveryWindow {
			continue
		}
		item.recoveryGrant = grant
		preferred = append(preferred, item)
	}
	if len(preferred) == 0 {
		return order
	}
	return append(preferred, order...)
}

func (s *OpenAIGatewayService) tryCodexRecoverySession(ctx context.Context, req OpenAIAccountScheduleRequest, item openAIAccountCandidateScore) *AccountSelectionResult {
	cache, ok := s.cache.(CodexRecoverySessionCache)
	if !ok || !req.newCodexSession() || item.account == nil {
		return nil
	}
	available, err := cache.CodexRecoveryAvailable(ctx, item.account.ID, item.recoveryGrant)
	if err != nil || !available {
		return nil
	}
	fresh := s.recheckSelectedOpenAIAccountFromDB(ctx, item.account, req.GroupID, req.Platform, req.RequestedModel, req.RequireCompact, req.RequiredCapability)
	check := defaultOpenAIAccountScheduler{service: s}
	if fresh == nil || fresh.GetExtraString(CodexRecoveryGrantKey) != item.recoveryGrant ||
		fresh.Priority != item.account.Priority || !fresh.IsSchedulable() || !fresh.IsOpenAIOAuth() || strings.EqualFold(fresh.GetCredential("plan_type"), "free") ||
		!s.openAIAccountMatchesSchedulingGroup(fresh, req.GroupID) || !check.isAccountRequestCompatible(ctx, fresh, req) ||
		!s.isOpenAIAccountTransportCompatible(fresh, req.RequiredTransport, req.RequestedModel) ||
		s.isExcelBPSCoolingDown(fresh, req.RequestedModel) || s.isExcelBPSModelCoolingDown(fresh, req.RequestedModel) {
		return nil
	}
	slot, err := s.tryAcquireAccountSlot(ctx, fresh.ID, fresh.Concurrency, &fresh)
	if err != nil || slot == nil || !slot.Acquired {
		return nil
	}
	selected, err := s.newAcquiredSelectionResult(ctx, fresh, slot.ReleaseFunc)
	if err != nil {
		return nil
	}
	binding := CodexRecoveryBinding{
		GroupID: derefGroupID(req.GroupID), Primary: s.openAISessionCacheKey(req.SessionHash),
		TTL: s.openAIWSSessionStickyTTL(), BindNow: !gatewayProfitControlGateActive(ctx),
		WriteLegacy: s.openAISessionHashDualWriteOldEnabled(),
	}
	if binding.WriteLegacy || s.openAISessionHashReadOldFallbackEnabled() {
		binding.Legacy = s.openAILegacySessionCacheKey(ctx, req.SessionHash)
		binding.LegacyTTL = s.openAIStickyLegacyTTL(binding.TTL)
	}
	claimed, err := cache.ClaimCodexRecoverySession(ctx, fresh.ID, item.recoveryGrant, binding)
	if err != nil || !claimed {
		if selected.ReleaseFunc != nil {
			selected.ReleaseFunc()
		}
		return nil
	}
	return selected
}
