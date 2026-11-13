package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIExcelOAuthRepository struct{ db *sql.DB }

func NewOpenAIExcelOAuthRepository(db *sql.DB) service.OpenAIExcelOAuthRepository {
	return &openAIExcelOAuthRepository{db: db}
}
func (r *openAIExcelOAuthRepository) SaveExcelCredentials(ctx context.Context, id int64, expected map[string]any, ciphertext string, diagnosis service.ExcelBPSCredentialState) (bool, error) {
	raw, err := json.Marshal(expected)
	if err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL AND platform='openai' AND type='oauth' AND credentials=$2::jsonb FOR UPDATE`, id, string(raw)).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET credentials_ciphertext=EXCLUDED.credentials_ciphertext,updated_at=NOW()`, id, ciphertext)
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=COALESCE(extra,'{}'::jsonb)-'openai_excel_bps_authorization_pending' WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	if err = writeExcelGrantDiagnosis(ctx, tx, id, []service.ExcelBPSCredentialState{diagnosis}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
func (r *openAIExcelOAuthRepository) ReplaceExcelCredentials(ctx context.Context, id int64, expected, replacement string, diagnosis ...service.ExcelBPSCredentialState) (bool, error) {
	return r.mutateExcelGrant(ctx, id, expected, replacement, false, diagnosis)
}
func (r *openAIExcelOAuthRepository) GetExcelCredentials(ctx context.Context, id int64) (string, error) {
	var ciphertext string
	err := r.db.QueryRowContext(ctx, `SELECT credentials_ciphertext FROM openai_excel_oauth_credentials WHERE account_id=$1`, id).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ciphertext, err
}

func (r *openAIExcelOAuthRepository) DeleteExcelCredentials(ctx context.Context, id int64, expected string, diagnosis ...service.ExcelBPSCredentialState) error {
	if len(diagnosis) == 0 {
		diagnosis = []service.ExcelBPSCredentialState{service.ExcelBPSGrantFailure("")}
	}
	_, err := r.mutateExcelGrant(ctx, id, expected, "", true, diagnosis)
	return err
}

// Lock accounts before grants, matching ApplyExcelCredentials. A stale response
// cannot change diagnostics or remove a newer ciphertext. Outbox is transactional.
func (r *openAIExcelOAuthRepository) mutateExcelGrant(ctx context.Context, id int64, expected, replacement string, remove bool, diagnosis []service.ExcelBPSCredentialState) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var locked int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var result sql.Result
	if remove {
		result, err = tx.ExecContext(ctx, `DELETE FROM openai_excel_oauth_credentials WHERE account_id=$1 AND credentials_ciphertext=$2`, id, expected)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE openai_excel_oauth_credentials SET credentials_ciphertext=$3,updated_at=NOW() WHERE account_id=$1 AND credentials_ciphertext=$2`, id, expected, replacement)
	}
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, nil
	}
	if err = writeExcelGrantDiagnosis(ctx, tx, id, diagnosis); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func writeExcelGrantDiagnosis(ctx context.Context, tx *sql.Tx, id int64, diagnosis []service.ExcelBPSCredentialState) error {
	state := service.ExcelBPSCredentialState{Status: "unknown"}
	if len(diagnosis) > 0 {
		state = diagnosis[0]
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=jsonb_set(COALESCE(extra,'{}'::jsonb),ARRAY['openai_excel_bps_credential_state'], $2::jsonb,true),updated_at=NOW() WHERE id=$1`, id, string(raw))
	if err != nil {
		return err
	}
	return enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil)
}
