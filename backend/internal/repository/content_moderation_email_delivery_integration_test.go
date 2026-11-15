//go:build integration

package repository

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestContentModerationPenaltyCurrentConfigAndAdminKeyScope(t *testing.T) {
	ctx := context.Background()
	repo := NewContentModerationRepository(integrationDB).(service.ContentModerationPenaltyRepository)
	var original string
	err := integrationDB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, service.SettingKeyContentModerationConfig).Scan(&original)
	require.True(t, err == nil || err == sql.ErrNoRows)
	existed := err == nil
	t.Cleanup(func() {
		if existed {
			_, _ = integrationDB.ExecContext(ctx, `UPDATE settings SET value=$2 WHERE key=$1`, service.SettingKeyContentModerationConfig, original)
		} else {
			_, _ = integrationDB.ExecContext(ctx, `DELETE FROM settings WHERE key=$1`, service.SettingKeyContentModerationConfig)
		}
	})
	setPolicy := func(raw string) {
		_, err := integrationDB.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, service.SettingKeyContentModerationConfig, raw)
		require.NoError(t, err)
	}
	var userID, keyID, otherID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,'test','admin') RETURNING id`, uuid.NewString()+"@admin-penalty.test").Scan(&userID))
	t.Cleanup(func() { _, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	for _, id := range []*int64{&keyID, &otherID} {
		require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys(user_id,key,name) VALUES($1,$2,'test') RETURNING id`, userID, uuid.NewString()).Scan(id))
	}
	cfg := &service.ContentModerationConfig{AutoBanEnabled: true, CyberPolicyBanMode: service.CyberPolicyBanImmediate, BanThreshold: 20, ViolationWindowHours: 720}
	makeEvent := func() *service.ContentModerationLog {
		return &service.ContentModerationLog{UserID: &userID, APIKeyID: &keyID, RequestID: uuid.NewString(), CreatedAt: time.Now(), Flagged: true, Action: service.ContentModerationActionCyberPolicy}
	}
	setPolicy(`{"auto_ban_enabled":false,"cyber_policy_ban_mode":"immediate"}`)
	out, err := repo.ApplyModerationPenalty(ctx, cfg, makeEvent())
	require.NoError(t, err)
	require.False(t, out.Transitioned)
	setPolicy(`{invalid`)
	_, err = repo.ApplyModerationPenalty(ctx, cfg, makeEvent())
	require.Error(t, err)
	setPolicy(`{"auto_ban_enabled":true,"cyber_policy_ban_mode":"immediate"}`)
	event := makeEvent()
	out, err = repo.ApplyModerationPenalty(ctx, cfg, event)
	require.NoError(t, err)
	require.True(t, out.Transitioned)
	require.Equal(t, "api_key", out.Target)
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM users WHERE id=$1`, userID).Scan(&status))
	require.Equal(t, "active", status)
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM api_keys WHERE id=$1`, otherID).Scan(&status))
	require.Equal(t, "active", status)
	_, err = integrationDB.ExecContext(ctx, `UPDATE api_keys SET status='active' WHERE id=$1`, keyID)
	require.NoError(t, err)
	event.RetryPenaltyDecision = true
	out, err = repo.ApplyModerationPenalty(ctx, cfg, event)
	require.NoError(t, err)
	require.False(t, out.Transitioned, "manually re-enabled administrator key must not be disabled by an old retry")
}

func TestContentModerationPenaltyAtomicCounterAndUnban(t *testing.T) {
	ctx := context.Background()
	repo := NewContentModerationRepository(integrationDB).(service.ContentModerationPenaltyRepository)
	var userID, keyID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'test') RETURNING id`, uuid.NewString()+"@penalty.test").Scan(&userID))
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO api_keys (user_id,key,name) VALUES ($1,$2,'test') RETURNING id`, userID, uuid.NewString()).Scan(&keyID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM content_moderation_logs WHERE user_id=$1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	cfg := &service.ContentModerationConfig{AutoBanEnabled: true, CyberPolicyBanMode: service.CyberPolicyBanCumulative, BanThreshold: 20, ViolationWindowHours: 720}
	event := func(request string) *service.ContentModerationLog {
		return &service.ContentModerationLog{RequestID: request, UserID: &userID, APIKeyID: &keyID, CreatedAt: time.Now(), Flagged: true, Action: service.ContentModerationActionCyberPolicy}
	}
	// Concurrent internal retries of one logical request only count once.
	var wg sync.WaitGroup
	failures := make(chan error, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.ApplyModerationPenalty(ctx, cfg, event("same-request"))
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	count, err := repo.CurrentModerationPenaltyCount(ctx, userID, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	// A different qualifying local event contributes to the same threshold.
	local := event("local-event")
	local.Action = service.ContentModerationActionBlock
	_, err = repo.ApplyModerationPenalty(ctx, cfg, local)
	require.NoError(t, err)
	for range 17 {
		out, err := repo.ApplyModerationPenalty(ctx, cfg, event(uuid.NewString()))
		require.NoError(t, err)
		require.False(t, out.Transitioned)
	}
	twentieth := event("twentieth")
	out, err := repo.ApplyModerationPenalty(ctx, cfg, twentieth)
	require.NoError(t, err)
	require.True(t, out.Transitioned)
	require.Equal(t, 20, out.Count)
	twentieth.RetryPenaltyDecision = true
	out, err = repo.ApplyModerationPenalty(ctx, cfg, twentieth)
	require.NoError(t, err)
	require.True(t, out.Transitioned, "recover a committed disposition after a lost response")
	cfg.AutoBanEnabled = false
	out, err = repo.ApplyModerationPenalty(ctx, cfg, twentieth)
	require.NoError(t, err)
	require.False(t, out.Transitioned, "current master also gates recovery side effects")
	cfg.AutoBanEnabled = true
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT status FROM api_keys WHERE id=$1`, keyID).Scan(&status))
	require.Equal(t, "disabled", status)
	// Every unban path uses the status trigger. Old pending work cannot re-ban.
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='active' WHERE id=$1`, userID)
	require.NoError(t, err)
	out, err = repo.ApplyModerationPenalty(ctx, cfg, twentieth)
	require.NoError(t, err)
	require.Equal(t, "before_unban", out.Status)
	require.False(t, out.Transitioned)
	count, err = repo.CurrentModerationPenaltyCount(ctx, userID, cfg)
	require.NoError(t, err)
	require.Zero(t, count)
	// Disabled-master decisions never become retroactive penalties.
	cfg.AutoBanEnabled = false
	off := event("master-off")
	out, err = repo.ApplyModerationPenalty(ctx, cfg, off)
	require.NoError(t, err)
	require.Equal(t, "not_counted", out.Status)
	cfg.AutoBanEnabled = true
	cfg.CyberPolicyBanMode = service.CyberPolicyBanImmediate
	out, err = repo.ApplyModerationPenalty(ctx, cfg, off)
	require.NoError(t, err)
	require.False(t, out.Transitioned)
	out, err = repo.ApplyModerationPenalty(ctx, cfg, event("new-immediate"))
	require.NoError(t, err)
	require.True(t, out.Transitioned)
	_, err = integrationDB.ExecContext(ctx, `UPDATE users SET status='active' WHERE id=$1`, userID)
	require.NoError(t, err)
	cfg.CyberPolicyBanMode = service.CyberPolicyBanCumulative
	var winners, failed atomic.Int64
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := repo.ApplyModerationPenalty(ctx, cfg, event(uuid.NewString()))
			if err != nil {
				failed.Add(1)
				return
			}
			if out.Transitioned {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failed.Load())
	require.Equal(t, int64(1), winners.Load())
	count, err = repo.CurrentModerationPenaltyCount(ctx, userID, cfg)
	require.NoError(t, err)
	require.Equal(t, 24, count, "concurrent distinct violations must not be lost")
}

func TestContentModerationPenaltyPreservesLegacyHistoryWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	base := NewContentModerationRepository(integrationDB)
	repo := base.(service.ContentModerationPenaltyRepository)
	var userID int64
	require.NoError(t, integrationDB.QueryRowContext(ctx, `INSERT INTO users (email,password_hash) VALUES ($1,'test') RETURNING id`, uuid.NewString()+"@legacy-penalty.test").Scan(&userID))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM content_moderation_logs WHERE user_id=$1`, userID)
		_, _ = integrationDB.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	cfg := &service.ContentModerationConfig{AutoBanEnabled: true, CyberPolicyBanMode: service.CyberPolicyBanCumulative, BanThreshold: 20, ViolationWindowHours: 720}
	var started time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT value::timestamptz FROM settings WHERE key='content_moderation_penalty_started_at'`).Scan(&started))
	for range 2 {
		require.NoError(t, base.CreateLog(ctx, &service.ContentModerationLog{RequestID: "legacy-request", UserID: &userID, Flagged: true, Action: service.ContentModerationActionCyberPolicy, CreatedAt: started.Add(-time.Second)}))
	}
	count, err := repo.CurrentModerationPenaltyCount(ctx, userID, cfg)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	log := &service.ContentModerationLog{RequestID: "legacy-request", UserID: &userID, Flagged: true, Action: service.ContentModerationActionCyberPolicy, CreatedAt: time.Now()}
	out, err := repo.ApplyModerationPenalty(ctx, cfg, log)
	require.NoError(t, err)
	require.Equal(t, 1, out.Count)
	cfg.CyberPolicyBanMode = service.CyberPolicyBanLogOnly
	count, err = repo.CurrentModerationPenaltyCount(ctx, userID, cfg)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestContentModerationEmailDeliveryClaimIsAtomicAcrossConnections(t *testing.T) {
	ctx := context.Background()
	repo := NewContentModerationRepository(integrationDB)
	log := &service.ContentModerationLog{
		RequestID: uuid.NewString(), Action: service.ContentModerationActionCyberPolicy,
		Flagged: true, DispositionStatus: "disabled", DispositionTarget: "user",
		DispositionTransitioned: true,
	}
	require.NoError(t, repo.CreateLog(ctx, log))
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM content_moderation_logs WHERE id = $1`, log.ID)
	})

	deliveryRepo := repo.(service.ContentModerationEmailDeliveryRepository)
	const workers = 32
	var winners atomic.Int64
	var failures atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			claim, err := deliveryRepo.ClaimLogEmailDelivery(ctx, log.ID)
			if err != nil {
				failures.Add(1)
				return
			}
			if claim.Claimed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Zero(t, failures.Load())
	require.Equal(t, int64(1), winners.Load())

	require.NoError(t, deliveryRepo.CompleteLogEmailDelivery(ctx, log.ID, true))
	claim, err := deliveryRepo.ClaimLogEmailDelivery(ctx, log.ID)
	require.NoError(t, err)
	require.True(t, claim.Exists)
	require.False(t, claim.Claimed)
	require.Equal(t, "sent", claim.Status)

	var status string
	var sent bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
SELECT email_delivery_status, email_sent FROM content_moderation_logs WHERE id = $1`, log.ID).Scan(&status, &sent))
	require.Equal(t, "sent", status)
	require.True(t, sent)
}
