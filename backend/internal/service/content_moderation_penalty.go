package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

const (
	CyberPolicyBanLogOnly    = "log_only"
	CyberPolicyBanCumulative = "cumulative"
	CyberPolicyBanImmediate  = "immediate"
)

// A single database transaction claims the request, counts it and changes status.
// Audit persistence and email delivery can be retried without repeating a penalty.
type ContentModerationPenaltyRepository interface {
	ApplyModerationPenalty(context.Context, *ContentModerationConfig, *ContentModerationLog) (ContentModerationPenaltyResult, error)
	CurrentModerationPenaltyCount(context.Context, int64, *ContentModerationConfig) (int, error)
}

type ContentModerationPenaltyResult struct {
	Count        int
	Status       string
	Target       string
	Transitioned bool
	Credentials  []string
}

func (cfg *ContentModerationConfig) cyberBanMode() string {
	if cfg == nil || !cfg.AutoBanEnabled {
		return CyberPolicyBanLogOnly
	}
	switch cfg.CyberPolicyBanMode {
	case CyberPolicyBanCumulative, CyberPolicyBanImmediate:
		return cfg.CyberPolicyBanMode
	default:
		return CyberPolicyBanLogOnly
	}
}

func (s *ContentModerationService) applyAtomicModerationPenalty(ctx context.Context, cfg *ContentModerationConfig, log *ContentModerationLog) (bool, bool, error) {
	repo, ok := s.repo.(ContentModerationPenaltyRepository)
	if !ok {
		return false, false, nil
	}
	if strings.TrimSpace(log.PenaltyEventKey) == "" && strings.TrimSpace(log.RequestID) == "" && log.ID == 0 && log.ArchiveID == "" {
		log.PenaltyEventKey = uuid.NewString()
	}
	result, err := repo.ApplyModerationPenalty(ctx, cfg, log)
	if err != nil {
		return true, false, err
	}
	log.ViolationCount = result.Count
	log.DispositionStatus = result.Status
	log.DispositionTarget = result.Target
	log.DispositionTransitioned = result.Transitioned
	log.AutoBanned = result.Transitioned && result.Target == "user"
	if result.Transitioned {
		if result.Target == "user" && log.Action == ContentModerationActionCyberPolicy {
			s.terminateCyberPolicyUserConversations(ctx, *log.UserID)
		}
		if s.authCacheInvalidator != nil {
			for _, credential := range result.Credentials {
				s.authCacheInvalidator.InvalidateAuthCacheByKey(ctx, credential)
			}
			if result.Target == "user" {
				s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, *log.UserID)
			}
		}
	}
	return true, result.Transitioned, nil
}
