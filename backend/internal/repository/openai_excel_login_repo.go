package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

var _ service.ExcelLoginRepository = (*openAIExcelOAuthRepository)(nil)

func (r *openAIExcelOAuthRepository) GetExcelLoginConfig(ctx context.Context, id int64) (*service.ExcelLoginConfig, error) {
	c := &service.ExcelLoginConfig{AccountID: id}
	err := r.db.QueryRowContext(ctx, `SELECT enabled,login_email,password_ciphertext,totp_ciphertext,revision FROM openai_excel_login_configs WHERE account_id=$1`, id).Scan(&c.Enabled, &c.LoginEmail, &c.PasswordCiphertext, &c.TOTPCiphertext, &c.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
func (r *openAIExcelOAuthRepository) SaveExcelLoginConfig(ctx context.Context, c *service.ExcelLoginConfig, expected int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND platform='openai' AND type='oauth' FOR UPDATE`, c.AccountID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO openai_excel_login_configs(account_id,enabled,login_email,password_ciphertext,totp_ciphertext) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.AccountID, c.Enabled, c.LoginEmail, c.PasswordCiphertext, c.TOTPCiphertext)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_configs SET enabled=$2,login_email=$3,password_ciphertext=$4,totp_ciphertext=$5,revision=revision+1,updated_at=NOW() WHERE account_id=$1 AND revision=$6`, c.AccountID, c.Enabled, c.LoginEmail, c.PasswordCiphertext, c.TOTPCiphertext, expected)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='cancelled',stage='cancelled',error_code='config_changed',updated_at=NOW() WHERE account_id=$1 AND status IN ('queued','running')`, c.AccountID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

const excelLoginTaskColumns = `id,account_id,status,stage,error_code,worker_id,config_revision,updated_at`

func scanExcelLoginTask(row interface{ Scan(...any) error }) (*service.ExcelLoginTask, error) {
	t := &service.ExcelLoginTask{}
	err := row.Scan(&t.ID, &t.AccountID, &t.Status, &t.Stage, &t.ErrorCode, &t.WorkerID, &t.ConfigRevision, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}
func (r *openAIExcelOAuthRepository) GetExcelLoginTask(ctx context.Context, id int64) (*service.ExcelLoginTask, error) {
	return scanExcelLoginTask(r.db.QueryRowContext(ctx, `SELECT `+excelLoginTaskColumns+` FROM openai_excel_login_tasks WHERE account_id=$1 ORDER BY id DESC LIMIT 1`, id))
}
func (r *openAIExcelOAuthRepository) QueueExcelLogin(ctx context.Context, id int64, manual bool) (*service.ExcelLoginTask, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND platform='openai' AND type='oauth' AND status='active' AND COALESCE(credentials->>'plan_type','') NOT ILIKE 'free' FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='failed',stage='failed',error_code='timeout',updated_at=NOW() WHERE account_id=$1 AND status IN ('queued','running') AND created_at<NOW()-INTERVAL '30 minutes'`, id)
	if err != nil {
		return nil, err
	}
	task, err := scanExcelLoginTask(tx.QueryRowContext(ctx, `INSERT INTO openai_excel_login_tasks(account_id,config_revision,expected_credentials_hash,expected_grant_hash)
 SELECT a.id,c.revision,MD5(a.credentials::text),MD5(COALESCE(g.credentials_ciphertext,''))
 FROM accounts a JOIN openai_excel_login_configs c ON c.account_id=a.id
 LEFT JOIN openai_excel_oauth_credentials g ON g.account_id=a.id
 WHERE a.id=$1 AND c.enabled AND c.password_ciphertext<>''
 AND ($2 OR (COALESCE(a.extra->>'openai_excel_bps','false')='true' AND g.account_id IS NULL))
 AND NOT EXISTS(SELECT 1 FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND
  (t.status IN ('queued','running') OR (t.status='failed' AND t.config_revision=c.revision AND t.updated_at>NOW()-INTERVAL '15 minutes')))
 AND ($2 OR (SELECT COUNT(*) FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND t.config_revision=c.revision AND t.status='failed')<3)
 ON CONFLICT DO NOTHING RETURNING `+excelLoginTaskColumns, id, manual))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return task, nil
}
func (r *openAIExcelOAuthRepository) ListMissingExcelLogins(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.id FROM accounts a JOIN openai_excel_login_configs c ON c.account_id=a.id
 LEFT JOIN openai_excel_oauth_credentials g ON g.account_id=a.id
 WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.status='active'
 AND COALESCE(a.credentials->>'plan_type','') NOT ILIKE 'free'
 AND c.enabled AND c.password_ciphertext<>'' AND COALESCE(a.extra->>'openai_excel_bps','false')='true' AND g.account_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND
 (t.status IN ('queued','running') OR (t.status='failed' AND t.config_revision=c.revision AND t.updated_at>NOW()-INTERVAL '15 minutes')))
 AND (SELECT COUNT(*) FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND t.config_revision=c.revision AND t.status='failed')<3
 ORDER BY a.id LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (r *openAIExcelOAuthRepository) ClaimExcelLogin(ctx context.Context, worker string) (*service.ExcelLoginTask, error) {
	return scanExcelLoginTask(r.db.QueryRowContext(ctx, `WITH candidate AS (
 SELECT t.id FROM openai_excel_login_tasks t JOIN accounts a ON a.id=t.account_id JOIN openai_excel_login_configs c ON c.account_id=a.id
 WHERE t.status='queued' AND t.created_at>NOW()-INTERVAL '30 minutes' AND a.deleted_at IS NULL AND a.status='active' AND c.enabled AND c.revision=t.config_revision
 ORDER BY t.id FOR UPDATE OF t SKIP LOCKED LIMIT 1)
 UPDATE openai_excel_login_tasks SET status='running',stage='starting',worker_id=$1,updated_at=NOW() WHERE id=(SELECT id FROM candidate) RETURNING `+excelLoginTaskColumns, worker))
}
func (r *openAIExcelOAuthRepository) ProgressExcelLogin(ctx context.Context, id int64, worker, stage string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET stage=$3,updated_at=NOW() WHERE id=$1 AND worker_id=$2 AND status='running' AND created_at>NOW()-INTERVAL '30 minutes'`, id, worker, stage)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (r *openAIExcelOAuthRepository) FailExcelLogin(ctx context.Context, id int64, worker, code string) (bool, error) {
	result, err := r.db.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='failed',stage='failed',error_code=$3,updated_at=NOW() WHERE id=$1 AND worker_id=$2 AND status='running'`, id, worker, code)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (r *openAIExcelOAuthRepository) FinishExcelLogin(ctx context.Context, id int64, worker string, candidate map[string]any, cipher string, state service.ExcelBPSCredentialState) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Same account -> config -> task lock order as saving settings. Grant refresh
	// and manual authorization hold this same account lock before changing grants.
	var accountID int64
	var primary []byte
	err = tx.QueryRowContext(ctx, `SELECT a.id,a.credentials FROM accounts a JOIN openai_excel_login_tasks t ON t.account_id=a.id WHERE t.id=$1 AND a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.status='active' FOR UPDATE OF a`, id).Scan(&accountID, &primary)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stored map[string]any
	if json.Unmarshal(primary, &stored) != nil {
		return false, errors.New("invalid stored account identity")
	}
	if err = service.ValidateExcelLoginIdentity(stored, candidate); err != nil {
		return false, err
	}
	var active int64
	err = tx.QueryRowContext(ctx, `SELECT t.id FROM openai_excel_login_tasks t JOIN openai_excel_login_configs c ON c.account_id=t.account_id JOIN accounts a ON a.id=t.account_id LEFT JOIN openai_excel_oauth_credentials g ON g.account_id=a.id
 WHERE t.id=$1 AND t.worker_id=$2 AND t.status='running' AND t.created_at>NOW()-INTERVAL '30 minutes' AND c.enabled AND c.revision=t.config_revision
 AND MD5(a.credentials::text)=t.expected_credentials_hash AND MD5(COALESCE(g.credentials_ciphertext,''))=t.expected_grant_hash FOR UPDATE OF c,t`, id, worker).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET credentials_ciphertext=EXCLUDED.credentials_ciphertext,updated_at=NOW()`, accountID, cipher)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)-'openai_excel_bps_authorization_pending' WHERE id=$1`, accountID)
	if err != nil {
		return false, err
	}
	if err = writeExcelGrantDiagnosis(ctx, tx, accountID, []service.ExcelBPSCredentialState{state}); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='succeeded',stage='succeeded',error_code='',updated_at=NOW() WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Resume an explicit reauthorization after restart even when an old grant still exists.
func (r *openAIExcelOAuthRepository) HasActiveExcelLogins(ctx context.Context) (bool, error) {
	_, err := r.db.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='failed',stage='failed',error_code='timeout',updated_at=NOW() WHERE status IN ('queued','running') AND created_at<NOW()-INTERVAL '30 minutes'`)
	if err != nil {
		return false, err
	}
	var active bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM openai_excel_login_tasks t JOIN openai_excel_login_configs c ON c.account_id=t.account_id JOIN accounts a ON a.id=t.account_id WHERE t.status IN ('queued','running') AND c.enabled AND c.revision=t.config_revision AND a.deleted_at IS NULL AND a.status='active')`).Scan(&active)
	return active, err
}

// Bring pre-existing switches into the same automatic preparation state. The
// bounded transaction also invalidates scheduler snapshots before login runs.
func (r *openAIExcelOAuthRepository) PrepareMissingExcelRoutes(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `WITH candidates AS (
 SELECT a.id FROM accounts a WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth'
 AND a.extra->'openai_excel_bps'='true'::jsonb
 AND COALESCE(a.extra->'openai_excel_bps_authorization_pending','false'::jsonb)<>'true'::jsonb
 AND COALESCE(a.credentials->>'client_id','')<>'app_fnr0pYvVwwFDocDumLG3H2Bp'
 AND NOT EXISTS(SELECT 1 FROM openai_excel_oauth_credentials e WHERE e.account_id=a.id AND e.credentials_ciphertext<>'')
 ORDER BY a.id LIMIT 50 FOR UPDATE OF a SKIP LOCKED)
 UPDATE accounts SET extra=extra||'{"openai_excel_bps_authorization_pending":true}'::jsonb,updated_at=NOW()
 WHERE id IN (SELECT id FROM candidates) RETURNING id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	// A save racing the successful callback may carry an older preparation
	// marker. Reconcile it from the persisted grant without another login.
	readyRows, err := tx.QueryContext(ctx, `UPDATE accounts a SET extra=extra-'openai_excel_bps_authorization_pending',updated_at=NOW()
 WHERE a.id IN (SELECT b.id FROM accounts b WHERE b.deleted_at IS NULL
 AND b.extra->'openai_excel_bps'='true'::jsonb AND b.extra->'openai_excel_bps_authorization_pending'='true'::jsonb
 AND EXISTS(SELECT 1 FROM openai_excel_oauth_credentials e WHERE e.account_id=b.id AND e.credentials_ciphertext<>'')
 ORDER BY b.id LIMIT 50 FOR UPDATE OF b SKIP LOCKED) RETURNING a.id`)
	if err != nil {
		return err
	}
	for readyRows.Next() {
		var id int64
		if err := readyRows.Scan(&id); err != nil {
			_ = readyRows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = readyRows.Err()
	_ = readyRows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
	}
	return tx.Commit()
}
