package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

var _ service.CodexLoginRepository = (*openAIExcelOAuthRepository)(nil)

const codexLoginAccountSQL = `a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth'
 AND COALESCE(a.credentials->>'client_id','') IN ('','app_EMoamEEZ73f0CkXaXp7hrann')
 AND LOWER(TRIM(COALESCE(a.credentials->>'auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token','agentidentity')
 AND LOWER(TRIM(COALESCE(a.credentials->>'openai_auth_mode',''))) NOT IN ('personalaccesstoken','personal_access_token')`

const codexLoginRecoverableSQL = `a.status='error'
 AND LOWER(COALESCE(a.error_message,'')) !~ '(account_deactivated|account_deleted|account_retired|cyber_policy)'
 AND (a.error_message='Codex authorization pending' OR a.error_message LIKE 'Token revoked (401):%' OR a.error_message LIKE 'Unauthorized (401):%' OR a.error_message LIKE 'Authentication failed (401):%'
 OR (a.error_message LIKE 'Token refresh failed (non-retryable):%' AND a.error_message ~ '(refresh_token_reused|refresh_token_invalidated|invalid_grant|invalid_refresh_token)'))`

const codexLoginAccountStateSQL = `jsonb_build_object('status',a.status,'error',COALESCE(a.error_message,''),'schedulable',a.schedulable,'proxy_id',a.proxy_id)
 || CASE WHEN a.extra->'openai_codex_auth_auto_paused'='true'::jsonb THEN '{"auth_auto_paused":true}'::jsonb ELSE '{}'::jsonb END`

// Explicit manual authorization remains possible after the business deadline.
// Old tasks without the marker are conservatively subject to the same deadline.
const codexLoginTaskWithinExpirySQL = `(t.expected_account_state->'manual_authorization'='true'::jsonb OR a.expires_at IS NULL OR a.expires_at>NOW())`

func (r *openAIExcelOAuthRepository) GetCodexAutoRecoveryStates(ctx context.Context, ids []int64) (map[int64]bool, error) {
	states := make(map[int64]bool)
	if len(ids) == 0 {
		return states, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT a.id,COALESCE(c.enabled,FALSE)
 FROM accounts a LEFT JOIN openai_codex_login_configs c ON c.account_id=a.id
 WHERE a.id=ANY($1) AND `+codexLoginAccountSQL, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var enabled bool
		if err := rows.Scan(&id, &enabled); err != nil {
			return nil, err
		}
		states[id] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return states, nil
}

func (r *openAIExcelOAuthRepository) GetCodexLoginConfig(ctx context.Context, id int64) (*service.ExcelLoginConfig, error) {
	c := &service.ExcelLoginConfig{AccountID: id}
	err := r.db.QueryRowContext(ctx, `SELECT enabled,login_email,password_ciphertext,totp_ciphertext,revision,preferred_workspace_id FROM openai_codex_login_configs WHERE account_id=$1`, id).Scan(&c.Enabled, &c.LoginEmail, &c.PasswordCiphertext, &c.TOTPCiphertext, &c.Revision, &c.PreferredWorkspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}
func (r *openAIExcelOAuthRepository) SaveCodexLoginConfig(ctx context.Context, c *service.ExcelLoginConfig, expected int64, expiresAt *int64) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a WHERE a.id=$1 AND `+codexLoginAccountSQL+`
 AND (COALESCE(a.credentials->>'email','')='' OR LOWER(a.credentials->>'email')=LOWER($2)) FOR UPDATE`, c.AccountID, c.LoginEmail).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO openai_codex_login_configs(account_id,enabled,login_email,password_ciphertext,totp_ciphertext,preferred_workspace_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, id, c.Enabled, c.LoginEmail, c.PasswordCiphertext, c.TOTPCiphertext, c.PreferredWorkspaceID)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE openai_codex_login_configs SET enabled=$2,login_email=$3,password_ciphertext=$4,totp_ciphertext=$5,preferred_workspace_id=$7,revision=revision+1,updated_at=NOW() WHERE account_id=$1 AND revision=$6`, id, c.Enabled, c.LoginEmail, c.PasswordCiphertext, c.TOTPCiphertext, expected, c.PreferredWorkspaceID)
	}
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if expiresAt != nil {
		var expiry *time.Time
		if *expiresAt > 0 {
			t := time.Unix(*expiresAt, 0)
			expiry = &t
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET expires_at=$2,auto_pause_on_expired=FALSE,updated_at=NOW() WHERE id=$1`, id, expiry)
		if err != nil {
			return false, err
		}
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='cancelled',stage='cancelled',error_code='config_changed',updated_at=NOW() WHERE account_id=$1 AND oauth_profile='codex' AND status IN ('queued','running')`, id)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (r *openAIExcelOAuthRepository) FindCodexLoginAccount(ctx context.Context, email string) (int64, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE deleted_at IS NULL AND platform='openai' AND type='oauth' AND LOWER(credentials->>'email')=LOWER($1) ORDER BY id LIMIT 1`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
func (r *openAIExcelOAuthRepository) GetCodexLoginTask(ctx context.Context, id int64) (*service.ExcelLoginTask, error) {
	return scanExcelLoginTask(r.db.QueryRowContext(ctx, `SELECT `+excelLoginTaskColumns+` FROM openai_excel_login_tasks WHERE account_id=$1 AND oauth_profile='codex' ORDER BY id DESC LIMIT 1`, id))
}
func (r *openAIExcelOAuthRepository) GetLoginTaskByID(ctx context.Context, id int64) (*service.ExcelLoginTask, error) {
	return scanExcelLoginTask(r.db.QueryRowContext(ctx, `SELECT `+excelLoginTaskColumns+` FROM openai_excel_login_tasks WHERE id=$1`, id))
}

// Allow the initial attempt plus three automatic retries, with a 60-second
// cooldown after each failure. Successful tasks reset the failure budget;
// configuration edits create a new revision. Manual retries still observe the
// cooldown and active task lock.
const codexLoginBudgetSQL = `NOT EXISTS(SELECT 1 FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND
 (t.status IN ('queued','running') OR (t.oauth_profile='codex' AND t.status='failed' AND t.config_revision=c.revision AND t.updated_at>NOW()-INTERVAL '60 seconds')))
 AND ($2 OR (SELECT COUNT(*) FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND t.oauth_profile='codex'
 AND t.config_revision=c.revision AND t.status='failed' AND t.id>COALESCE((SELECT MAX(ok.id) FROM openai_excel_login_tasks ok WHERE ok.account_id=a.id AND ok.oauth_profile='codex' AND ok.status='succeeded'),0))<4)
 AND ($2 OR NOT EXISTS(SELECT 1 FROM openai_excel_login_tasks t WHERE t.account_id=a.id AND t.oauth_profile='codex' AND t.config_revision=c.revision
 AND t.status='failed' AND t.error_code IN ('password_rejected','totp_rejected','identity_mismatch','workspace_unavailable','workspace_selection_required','security_check','additional_verification','account_blocked','credentials_unavailable','worker_failed','browser_unavailable','browser_start_failed','browser_dependency_failed')
 AND t.id>COALESCE((SELECT MAX(ok.id) FROM openai_excel_login_tasks ok WHERE ok.account_id=a.id AND ok.oauth_profile='codex' AND ok.status='succeeded'),0)))`

func (r *openAIExcelOAuthRepository) QueueCodexLogin(ctx context.Context, id int64, manual bool) (*service.ExcelLoginTask, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT a.id FROM accounts a WHERE a.id=$1 AND `+codexLoginAccountSQL+` AND a.status IN ('active','error') FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='failed',stage='failed',error_code='timeout',updated_at=NOW() WHERE account_id=$1 AND oauth_profile='codex' AND status IN ('queued','running') AND created_at<NOW()-INTERVAL '30 minutes'`, id)
	if err != nil {
		return nil, err
	}
	t, err := scanExcelLoginTask(tx.QueryRowContext(ctx, `INSERT INTO openai_excel_login_tasks(account_id,config_revision,expected_credentials_hash,expected_grant_hash,oauth_profile,expected_account_state)
 SELECT a.id,c.revision,MD5(a.credentials::text),'','codex',`+codexLoginAccountStateSQL+` || CASE WHEN $2 THEN '{"manual_authorization":true}'::jsonb ELSE '{}'::jsonb END
 FROM accounts a JOIN openai_codex_login_configs c ON c.account_id=a.id
 WHERE a.id=$1 AND c.password_ciphertext<>'' AND ($2 OR (c.enabled AND (a.expires_at IS NULL OR a.expires_at>NOW()) AND `+codexLoginRecoverableSQL+`)) AND `+codexLoginBudgetSQL+`
 ON CONFLICT DO NOTHING RETURNING `+excelLoginTaskColumns, id, manual))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return t, nil
}
func (r *openAIExcelOAuthRepository) ListCodexLoginCandidates(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT a.id FROM accounts a JOIN openai_codex_login_configs c ON c.account_id=a.id
 WHERE `+codexLoginAccountSQL+` AND `+codexLoginRecoverableSQL+` AND c.enabled AND c.password_ciphertext<>''
 AND (a.expires_at IS NULL OR a.expires_at>NOW()) AND a.id>$1 AND `+codexLoginBudgetSQL+`
 ORDER BY a.id LIMIT 50`, 0, false)
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
func (r *openAIExcelOAuthRepository) HasActiveCodexLogins(ctx context.Context) (bool, error) {
	_, err := r.db.ExecContext(ctx, `UPDATE openai_excel_login_tasks t SET status='cancelled',stage='cancelled',error_code=CASE WHEN NOT COALESCE(`+codexLoginTaskWithinExpirySQL+`,FALSE) THEN 'account_expired' ELSE 'account_changed' END,updated_at=NOW()
 FROM accounts a LEFT JOIN openai_codex_login_configs c ON c.account_id=a.id
 WHERE t.account_id=a.id AND t.oauth_profile='codex' AND t.status IN ('queued','running')
 AND (a.deleted_at IS NOT NULL OR c.revision IS DISTINCT FROM t.config_revision OR MD5(a.credentials::text) IS DISTINCT FROM t.expected_credentials_hash
 OR NOT COALESCE(`+codexLoginTaskWithinExpirySQL+`,FALSE)
 OR `+codexLoginAccountStateSQL+` IS DISTINCT FROM (t.expected_account_state-'manual_authorization'))`)
	if err != nil {
		return false, err
	}
	_, err = r.db.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='failed',stage='failed',error_code='timeout',updated_at=NOW() WHERE oauth_profile='codex' AND status IN ('queued','running') AND created_at<NOW()-INTERVAL '30 minutes'`)
	if err != nil {
		return false, err
	}
	var active bool
	err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM openai_excel_login_tasks t JOIN openai_codex_login_configs c ON c.account_id=t.account_id JOIN accounts a ON a.id=t.account_id WHERE t.oauth_profile='codex' AND t.status IN ('queued','running') AND c.revision=t.config_revision AND a.deleted_at IS NULL AND a.status IN ('active','error'))`).Scan(&active)
	return active, err
}

var managedCodexWorkerID = regexp.MustCompile(`^managed-[0-9]{1,10}-[0-9]{1,20}(-slot-[0-9]{1,2})?$`)

func codexWorkerLogID(id string) string {
	if managedCodexWorkerID.MatchString(id) {
		return id
	}
	return "other"
}

// The snapshot may contain free-form upstream error text. Log only its class.
func codexLoginTriggerReason(msg string) string {
	switch {
	case msg == service.CodexAuthorizationPending:
		return "initial_pending"
	case strings.HasPrefix(msg, "Token revoked (401):"):
		return "revoked_401"
	case strings.HasPrefix(msg, "Unauthorized (401):"), strings.HasPrefix(msg, "Authentication failed (401):"):
		return "unauthorized_401"
	case strings.HasPrefix(msg, "Token refresh failed (non-retryable):"):
		for _, code := range []string{"refresh_token_reused", "refresh_token_invalidated", "invalid_refresh_token", "invalid_grant"} {
			if strings.Contains(msg, code) {
				return code
			}
		}
		return "refresh_failed"
	default:
		return "other"
	}
}

func (r *openAIExcelOAuthRepository) ClaimCodexLogin(ctx context.Context, worker string) (*service.ExcelLoginTask, error) {
	task, err := scanExcelLoginTask(r.db.QueryRowContext(ctx, `WITH candidate AS (
 SELECT t.id FROM openai_excel_login_tasks t JOIN accounts a ON a.id=t.account_id JOIN openai_codex_login_configs c ON c.account_id=a.id
 WHERE t.oauth_profile='codex' AND t.status='queued' AND t.created_at>NOW()-INTERVAL '30 minutes' AND a.deleted_at IS NULL AND a.status IN ('active','error') AND c.revision=t.config_revision
 AND MD5(a.credentials::text)=t.expected_credentials_hash
 AND `+codexLoginTaskWithinExpirySQL+`
 AND `+codexLoginAccountStateSQL+`=(t.expected_account_state-'manual_authorization')
 ORDER BY t.id FOR UPDATE OF t SKIP LOCKED LIMIT 1)
 UPDATE openai_excel_login_tasks SET status='running',stage='starting',worker_id=$1,updated_at=NOW() WHERE id=(SELECT id FROM candidate) RETURNING `+excelLoginTaskColumns, worker))
	if err == nil && task != nil {
		slog.InfoContext(ctx, "codex_login_claimed", "task_id", task.ID, "account_id", task.AccountID,
			"worker_id", codexWorkerLogID(worker), "claimed_at", task.UpdatedAt)
	}
	return task, err
}
func (r *openAIExcelOAuthRepository) FinishCodexLogin(ctx context.Context, id int64, worker string, candidate map[string]any, astra *service.CodexLoginAstraResult) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var accountID int64
	var primary, extraJSON []byte
	var status, message string
	err = tx.QueryRowContext(ctx, `SELECT a.id,a.credentials,a.status,COALESCE(a.error_message,''),COALESCE(a.extra,'{}'::jsonb) FROM accounts a JOIN openai_excel_login_tasks t ON t.account_id=a.id WHERE t.id=$1 AND t.oauth_profile='codex' AND a.deleted_at IS NULL AND a.platform='openai' AND a.type='oauth' AND a.status IN ('active','error') FOR UPDATE OF a`, id).Scan(&accountID, &primary, &status, &message, &extraJSON)
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
	tokenInfo, err := service.ValidateCodexLoginIdentity(stored, candidate)
	if err != nil {
		return false, err
	}
	var active int64
	err = tx.QueryRowContext(ctx, `SELECT t.id FROM openai_excel_login_tasks t JOIN openai_codex_login_configs c ON c.account_id=t.account_id JOIN accounts a ON a.id=t.account_id
 WHERE t.id=$1 AND t.oauth_profile='codex' AND t.worker_id=$2 AND t.status='running' AND t.created_at>NOW()-INTERVAL '30 minutes' AND c.revision=t.config_revision
 AND MD5(a.credentials::text)=t.expected_credentials_hash
 AND `+codexLoginTaskWithinExpirySQL+`
 AND `+codexLoginAccountStateSQL+`=(t.expected_account_state-'manual_authorization')
 AND (COALESCE(a.credentials->>'chatgpt_account_id','')<>'' OR c.preferred_workspace_id='' OR c.preferred_workspace_id=$3) FOR UPDATE OF c,t`, id, worker, candidate["chatgpt_account_id"]).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Merge only verified token fields; preserve model mappings, proxy/group settings
	// and any explicit administrator scheduling pause.
	info := make(map[string]any, len(candidate)+1)
	for key, value := range candidate {
		info[key] = value
	}
	delete(info, service.CodexWorkspaceFallbackFrom)
	delete(info, service.CodexPersonalWorkspaceOnly)
	info["plan_type"] = tokenInfo.PlanType
	info["_token_version"] = time.Now().UnixMilli()
	raw, err := json.Marshal(info)
	if err != nil {
		return false, err
	}
	recoverState := service.CodexLoginCanRecoverState(status, message)
	// Count only successful browser reauthorization of an already bound account.
	// The row/task locks and state checks above make repeated callbacks a no-op.
	reauthorization := service.CodexLoginIsReauthorization(stored, message)
	freePlanRecovery := reauthorization && tokenInfo.PlanType == "free"
	if !reauthorization || freePlanRecovery {
		astra = nil
	} else if astra == nil {
		astra = &service.CodexLoginAstraResult{Verdict: "unknown"}
	}
	probeExtra := map[string]any{}
	if astra != nil {
		var extra map[string]any
		if err := json.Unmarshal(extraJSON, &extra); err != nil {
			return false, err
		}
		if history := astra.History(extra[service.OpenAIAstraPolicyExtraKey]); history != nil {
			probeExtra[service.OpenAIAstraPolicyExtraKey] = history
		}
	}
	probeJSON, err := json.Marshal(probeExtra)
	if err != nil {
		return false, err
	}
	// Do not expose the new token to routing until its fresh Astra result is
	// known. Failed/unknown probes keep valid credentials but pause scheduling;
	// healthy probes never undo an administrator's existing pause. A recovered
	// Free plan stays paused even if a worker supplies a healthy Astra result.
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET credentials=credentials||$2::jsonb,
 status=CASE WHEN $3 THEN 'active' ELSE status END,
 error_message=CASE WHEN $3 THEN NULL ELSE error_message END,
 schedulable=CASE WHEN $6 THEN FALSE
 WHEN $8 AND extra->'openai_codex_auth_auto_paused'='true'::jsonb THEN TRUE
 WHEN error_message=$4 AND extra->'openai_codex_authorization_pending'='true'::jsonb THEN TRUE ELSE schedulable END,
	 extra=(COALESCE(extra,'{}'::jsonb)-'openai_codex_authorization_pending'-'openai_codex_auth_auto_paused') || CASE WHEN $5 THEN
	 jsonb_build_object('openai_codex_reauthorization_count', CASE
	 WHEN jsonb_typeof(extra->'openai_codex_reauthorization_count')='number'
	 AND (extra->>'openai_codex_reauthorization_count') ~ '^[0-9]{1,15}$'
	 THEN (extra->>'openai_codex_reauthorization_count')::bigint ELSE 0 END + 1)
		 ELSE '{}'::jsonb END || $7::jsonb,updated_at=NOW() WHERE id=$1`, accountID, string(raw), recoverState, service.CodexAuthorizationPending, reauthorization, freePlanRecovery || (astra != nil && !astra.Healthy()), string(probeJSON), recoverState && astra != nil && astra.Healthy())
	if err != nil {
		return false, err
	}
	if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &accountID, nil, nil); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE openai_excel_login_tasks SET status='succeeded',stage=$2,error_code='',updated_at=NOW() WHERE id=$1`, id, service.CodexLoginCompletionStage(candidate, astra, freePlanRecovery))
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
