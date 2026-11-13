package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"time"
)

func (r *openAIExcelOAuthRepository) ApplyExcelBPSAutoPolicy(ctx context.Context, id int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	a := &service.Account{ID: id}
	var credentials, extra []byte
	err = tx.QueryRowContext(ctx, `SELECT platform,type,status,COALESCE(credentials,'{}'::jsonb),COALESCE(extra,'{}'::jsonb) FROM accounts WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&a.Platform, &a.Type, &a.Status, &credentials, &extra)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(credentials, &a.Credentials); err != nil {
		return err
	}
	if err = json.Unmarshal(extra, &a.Extra); err != nil {
		return err
	}
	var previous []byte
	err = tx.QueryRowContext(ctx, `SELECT state FROM openai_excel_bps_auto_states WHERE account_id=$1`, id).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var state service.ExcelBPSAutoState
	if len(previous) > 0 {
		if err = json.Unmarshal(previous, &state); err != nil {
			return err
		}
	}
	var ready bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM openai_excel_oauth_credentials WHERE account_id=$1 AND credentials_ciphertext<>'')`, id).Scan(&ready); err != nil {
		return err
	}
	next, patch, err := service.EvaluateExcelBPSAutoPolicy(a, state, ready, now)
	if err != nil {
		return err
	}
	if len(patch) > 0 {
		set := map[string]any{}
		remove := []string{}
		for k, v := range patch {
			if v == nil {
				remove = append(remove, k)
			} else {
				set[k] = v
			}
		}
		raw, e := json.Marshal(set)
		if e != nil {
			return e
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra=(COALESCE(extra,'{}'::jsonb)||$2::jsonb)-$3::text[],updated_at=NOW() WHERE id=$1`, id, string(raw), pq.Array(remove))
		if err != nil {
			return err
		}
		if err = enqueueSchedulerOutbox(ctx, tx, service.SchedulerOutboxEventAccountChanged, &id, nil, nil); err != nil {
			return err
		}
	}
	if next.ConfigHash != "" {
		raw, e := json.Marshal(next)
		if e != nil {
			return e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO openai_excel_bps_auto_states(account_id,state) VALUES($1,$2::jsonb) ON CONFLICT(account_id) DO UPDATE SET state=EXCLUDED.state,updated_at=NOW() WHERE openai_excel_bps_auto_states.state IS DISTINCT FROM EXCLUDED.state`, id, string(raw))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
