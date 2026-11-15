package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *contentModerationRepository) CleanupModerationPenaltyEvents(ctx context.Context) error {
	for {
		result, err := r.db.ExecContext(ctx, `DELETE FROM content_moderation_penalty_events WHERE (user_id, event_key) IN (SELECT user_id, event_key FROM content_moderation_penalty_events WHERE occurred_at < NOW() - INTERVAL '366 days' LIMIT 1000)`)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n < 1000 {
			return nil
		}
	}
}

func moderationEventKey(log *service.ContentModerationLog) string {
	if request := strings.TrimSpace(log.RequestID); request != "" {
		sum := sha256.Sum256([]byte(request))
		return "request:" + hex.EncodeToString(sum[:])
	}
	if log.PenaltyEventKey != "" {
		return "event:" + log.PenaltyEventKey
	}
	if log.ArchiveID != "" {
		return "archive:" + log.ArchiveID
	}
	if log.ID > 0 {
		return fmt.Sprintf("log:%d", log.ID)
	}
	return "event:" + log.PenaltyEventKey
}

// Union pre-upgrade history with new decisions, without recounting their audit
// rows. Explicit non-counted decisions remain excluded after a policy change.
const moderationPenaltyCountSQL = `
WITH events AS (
    SELECT event_key, occurred_at, cyber FROM content_moderation_penalty_events
    WHERE user_id = $1 AND counted
    UNION
    SELECT CASE WHEN BTRIM(request_id) <> ''
                THEN 'request:' || encode(sha256(convert_to(BTRIM(request_id), 'UTF8')), 'hex')
                WHEN archive_id IS NOT NULL THEN 'archive:' || archive_id::text
                ELSE 'log:' || id::text END, created_at, action = 'cyber_policy'
    FROM content_moderation_logs l
    WHERE user_id = $1 AND flagged AND NOT cache_hit
      AND created_at < (SELECT value::timestamptz FROM settings WHERE key = 'content_moderation_penalty_started_at')
      AND action NOT IN ('hash_block', 'cache_block') AND action NOT LIKE 'security_policy_%'
      AND mode NOT IN ('cyber_log_only', 'risk_control_log_only')
      AND decision_source <> 'cache_replay' AND disposition_status <> 'not_counted'
      AND NOT EXISTS (
          SELECT 1 FROM content_moderation_penalty_events e WHERE e.user_id = l.user_id
          AND e.event_key = CASE WHEN BTRIM(l.request_id) <> ''
              THEN 'request:' || encode(sha256(convert_to(BTRIM(l.request_id), 'UTF8')), 'hex')
              WHEN l.archive_id IS NOT NULL THEN 'archive:' || l.archive_id::text
              ELSE 'log:' || l.id::text END
      )
)
SELECT COUNT(DISTINCT event_key) FROM events
WHERE occurred_at >= $2 AND occurred_at > COALESCE($3::timestamptz, '-infinity'::timestamptz)
  AND (NOT cyber OR $4::bool)`

func (r *contentModerationRepository) CurrentModerationPenaltyCount(ctx context.Context, userID int64, cfg *service.ContentModerationConfig) (int, error) {
	var reset sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT content_moderation_reset_at FROM users WHERE id = $1`, userID).Scan(&reset); err != nil {
		return 0, err
	}
	var count int
	err := r.db.QueryRowContext(ctx, moderationPenaltyCountSQL, userID,
		time.Now().Add(-time.Duration(cfg.ViolationWindowHours)*time.Hour), reset,
		cfg.CyberPolicyBanMode == service.CyberPolicyBanCumulative).Scan(&count)
	return count, err
}

func (r *contentModerationRepository) ApplyModerationPenalty(ctx context.Context, cfg *service.ContentModerationConfig, log *service.ContentModerationLog) (service.ContentModerationPenaltyResult, error) {
	out := service.ContentModerationPenaltyResult{Status: "not_counted"}
	if cfg == nil || log == nil || log.UserID == nil {
		return out, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer func() { _ = tx.Rollback() }()
	var status, role string
	var reset sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT status, role, content_moderation_reset_at FROM users WHERE id = $1 AND deleted_at IS NULL FOR UPDATE`, *log.UserID).Scan(&status, &role, &reset)
	if err != nil {
		return out, err
	}
	// Do not punish using the one-second runtime cache after an admin switches off.
	current := *cfg
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = $1`, service.SettingKeyContentModerationConfig).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return out, err
	}
	if err == nil {
		current.CyberPolicyBanMode = service.CyberPolicyBanLogOnly
		if err = json.Unmarshal([]byte(raw), &current); err != nil {
			return out, err
		}
	}
	cfg = &current
	if log.CreatedAt.IsZero() {
		return out, fmt.Errorf("moderation penalty event time is required")
	}
	// All paths that reactivate a user set this boundary via the database trigger.
	if reset.Valid && !log.CreatedAt.After(reset.Time) {
		out.Status = "before_unban"
		return out, nil
	}
	window := cfg.ViolationWindowHours
	if window <= 0 {
		window = 720
	}
	if window > 8760 {
		window = 8760
	}
	since := time.Now().Add(-time.Duration(window) * time.Hour)
	if log.CreatedAt.Before(since) {
		out.Status = "expired"
		return out, nil
	}
	// Old retry files did not include request identity. Recover it from the audit row.
	if log.RequestID == "" && (log.ID > 0 || log.ArchiveID != "") {
		err = tx.QueryRowContext(ctx, `SELECT request_id FROM content_moderation_logs WHERE user_id = $1 AND (id = $2 OR archive_id::text = NULLIF($3, '')) ORDER BY id LIMIT 1`, *log.UserID, log.ID, log.ArchiveID).Scan(&log.RequestID)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
	}
	cyber := log.Action == service.ContentModerationActionCyberPolicy
	counted := cfg.AutoBanEnabled && log.Flagged && !log.CacheHit && log.DecisionSource != "cache_replay" &&
		log.Action != "hash_block" && log.Action != "cache_block" && !strings.HasPrefix(log.Action, "security_policy_") &&
		log.Mode != "cyber_log_only" && log.Mode != "risk_control_log_only"
	mode := cfg.CyberPolicyBanMode
	if cyber && mode != service.CyberPolicyBanCumulative && mode != service.CyberPolicyBanImmediate {
		counted = false
	}
	key := moderationEventKey(log)
	if key == "event:" {
		return out, fmt.Errorf("moderation penalty identity is required")
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO content_moderation_penalty_events (user_id, event_key, occurred_at, cyber, counted) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, *log.UserID, key, log.CreatedAt, cyber, counted)
	if err != nil {
		return out, err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, moderationPenaltyCountSQL, *log.UserID, since, reset, mode == service.CyberPolicyBanCumulative).Scan(&out.Count)
	if err != nil {
		return out, err
	}
	if claimed == 0 {
		out.Status = "duplicate"
		return out, tx.Commit()
	}
	if !counted {
		return out, tx.Commit()
	}
	out.Status = "not_required"
	immediate := cyber && mode == service.CyberPolicyBanImmediate
	if !immediate && (cfg.BanThreshold <= 0 || out.Count < cfg.BanThreshold) {
		return out, tx.Commit()
	}
	out.Target = "user"
	if role == service.RoleAdmin {
		if !cyber {
			out.Status = "skipped_admin"
			return out, tx.Commit()
		}
		out.Target = "api_key"
		if log.APIKeyID == nil {
			return out, fmt.Errorf("administrator Cyber penalty needs an API key")
		}
		var credential string
		err = tx.QueryRowContext(ctx, `UPDATE api_keys SET status = 'disabled', updated_at = NOW() WHERE id = $1 AND user_id = $2 AND status = 'active' AND deleted_at IS NULL RETURNING key`, *log.APIKeyID, *log.UserID).Scan(&credential)
		if err == sql.ErrNoRows {
			out.Status = "already_disabled"
			return out, tx.Commit()
		}
		if err != nil {
			return out, err
		}
		out.Credentials = append(out.Credentials, credential)
	} else {
		if status != "active" {
			out.Status = "already_disabled"
			return out, tx.Commit()
		}
		if _, err = tx.ExecContext(ctx, `UPDATE users SET status = 'disabled', updated_at = NOW() WHERE id = $1`, *log.UserID); err != nil {
			return out, err
		}
		if cyber {
			rows, err := tx.QueryContext(ctx, `UPDATE api_keys SET status = 'disabled', updated_at = NOW() WHERE user_id = $1 AND status = 'active' AND deleted_at IS NULL RETURNING key`, *log.UserID)
			if err != nil {
				return out, err
			}
			for rows.Next() {
				var credential string
				if err = rows.Scan(&credential); err != nil {
					_ = rows.Close()
					return out, err
				}
				out.Credentials = append(out.Credentials, credential)
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return out, err
			}
		}
	}
	out.Status, out.Transitioned = "disabled", true
	if err = tx.Commit(); err != nil {
		return service.ContentModerationPenaltyResult{}, err
	}
	return out, nil
}
