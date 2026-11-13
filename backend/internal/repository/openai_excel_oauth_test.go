//go:build unit

package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestExcelOAuthGrantSaveIsAtomicAndPreservesPrimary(t *testing.T) {
	for _, failure := range []string{"", "snapshot", "grant", "diagnosis", "outbox"} {
		t.Run(failure, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			repo := NewOpenAIExcelOAuthRepository(db)
			mock.ExpectBegin()
			row := mock.ExpectQuery("SELECT id FROM accounts.*credentials=.*FOR UPDATE").WithArgs(int64(7), `{"access_token":"primary"}`)
			if failure == "snapshot" {
				row.WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()
			} else {
				row.WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
				for _, step := range []struct{ name, sql string }{{"grant", "INSERT INTO openai_excel_oauth_credentials"}, {"pending", "UPDATE accounts SET extra=COALESCE"}, {"diagnosis", "UPDATE accounts SET extra=jsonb_set"}, {"outbox", "INSERT INTO scheduler_outbox"}} {
					exp := mock.ExpectExec(step.sql)
					if step.name == "grant" {
						exp.WithArgs(int64(7), "encrypted-grant")
					}
					if step.name == failure {
						exp.WillReturnError(errors.New("database unavailable"))
						break
					}
					exp.WillReturnResult(sqlmock.NewResult(0, 1))
				}
				if failure == "" {
					mock.ExpectCommit()
				} else {
					mock.ExpectRollback()
				}
			}
			changed, err := repo.SaveExcelCredentials(context.Background(), 7, map[string]any{"access_token": "primary"}, "encrypted-grant", service.ExcelBPSGrantState(123456))
			if failure != "" && failure != "snapshot" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, failure == "", changed)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestExcelOAuthStaleGrantCannotOverwriteOrDeleteReplacement(t *testing.T) {
	for _, remove := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		repo := NewOpenAIExcelOAuthRepository(db)
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT id FROM accounts.*FOR UPDATE").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
		if remove {
			mock.ExpectExec("DELETE FROM openai_excel_oauth_credentials.*credentials_ciphertext").WithArgs(int64(7), "old").WillReturnResult(sqlmock.NewResult(0, 0))
		} else {
			mock.ExpectExec("UPDATE openai_excel_oauth_credentials.*credentials_ciphertext").WithArgs(int64(7), "old", "stale").WillReturnResult(sqlmock.NewResult(0, 0))
		}
		mock.ExpectRollback()
		if remove {
			require.NoError(t, repo.DeleteExcelCredentials(context.Background(), 7, "old"))
		} else {
			changed, err := repo.ReplaceExcelCredentials(context.Background(), 7, "old", "stale")
			require.NoError(t, err)
			require.False(t, changed)
		}
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}

// A real PostgreSQL transaction is necessary here: sqlmock cannot prove SKIP
// LOCKED ownership, partial uniqueness or the row-lock/CAS ordering.
func TestExcelLoginPostgresQueueAndGrantGuards(t *testing.T) {
	dsn := os.Getenv("BPS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL is supplied by BPS Actions")
	}
	cfg, err := url.Parse(dsn)
	require.NoError(t, err)
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer admin.Close()
	schema := "bps_" + fmt.Sprintf("%x", uuid.New())
	_, err = admin.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	defer func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) }()
	params := cfg.Query()
	params.Set("search_path", schema)
	cfg.RawQuery = params.Encode()
	db, err := sql.Open("postgres", cfg.String())
	require.NoError(t, err)
	defer db.Close()
	ctx := context.Background()
	_, err = db.Exec(`CREATE TABLE accounts(id BIGINT PRIMARY KEY,platform TEXT DEFAULT 'openai',type TEXT DEFAULT 'oauth',status TEXT DEFAULT 'active',credentials JSONB,extra JSONB,deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ DEFAULT NOW()); CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT); CREATE UNIQUE INDEX dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	for _, migration := range []string{"276_excel_bps_independent_grant.sql", "277_excel_bps_authorization_queue.sql"} {
		raw, e := os.ReadFile("../../migrations/" + migration)
		require.NoError(t, e)
		_, e = db.Exec(string(raw))
		require.NoError(t, e)
	}
	repo := &openAIExcelOAuthRepository{db: db}
	primary := map[string]any{"access_token": "native-primary", "email": "user@example.com", "chatgpt_account_id": "workspace", "chatgpt_user_id": "user", "plan_type": "pro"}
	primaryJSON, _ := json.Marshal(primary)
	jwt := func(claims map[string]any) string {
		b, _ := json.Marshal(claims)
		return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".test"
	}
	candidate := map[string]any{"client_id": openai.ExcelClientID, "refresh_token": "excel-refresh", "email": "user@example.com", "chatgpt_account_id": "workspace", "chatgpt_user_id": "user"}
	candidate["access_token"] = jwt(map[string]any{"client_id": openai.ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user", "user_id": "user"}})
	candidate["id_token"] = jwt(map[string]any{"aud": []string{openai.ExcelClientID}, "email": "user@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user", "user_id": "user"}})
	for i, scenario := range []string{"success", "other worker", "manual grant", "primary changed", "config changed", "identity mismatch", "expired task"} {
		t.Run(scenario, func(t *testing.T) {
			id := int64(i + 1)
			_, e := db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES($1,$2,'{"openai_excel_bps":true}')`, id, string(primaryJSON))
			require.NoError(t, e)
			login := &service.ExcelLoginConfig{AccountID: id, Enabled: true, LoginEmail: "user@example.com", PasswordCiphertext: "encrypted-password"}
			saved, e := repo.SaveExcelLoginConfig(ctx, login, 0)
			require.NoError(t, e)
			require.True(t, saved)
			queued, e := repo.QueueExcelLogin(ctx, id, false)
			require.NoError(t, e)
			require.NotNil(t, queued)
			duplicate, e := repo.QueueExcelLogin(ctx, id, false)
			require.NoError(t, e)
			require.Nil(t, duplicate)
			claimed, e := repo.ClaimExcelLogin(ctx, "worker-a")
			require.NoError(t, e)
			require.NotNil(t, claimed)
			require.Equal(t, queued.ID, claimed.ID)
			claimAgain, e := repo.ClaimExcelLogin(ctx, "worker-b")
			require.NoError(t, e)
			require.Nil(t, claimAgain)
			creds := make(map[string]any)
			for k, v := range candidate {
				creds[k] = v
			}
			worker := "worker-a"
			switch scenario {
			case "other worker":
				worker = "worker-b"
			case "manual grant":
				_, e = repo.SaveExcelCredentials(ctx, id, primary, "new-manual-grant", service.ExcelBPSGrantState(123))
				require.NoError(t, e)
			case "primary changed":
				_, e = db.Exec(`UPDATE accounts SET credentials=jsonb_set(credentials,'{access_token}','"new-primary"') WHERE id=$1`, id)
				require.NoError(t, e)
			case "config changed":
				saved, e = repo.SaveExcelLoginConfig(ctx, login, 1)
				require.NoError(t, e)
				require.True(t, saved)
			case "identity mismatch":
				creds["access_token"] = jwt(map[string]any{"client_id": openai.ExcelClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "other", "user_id": "user"}})
			case "expired task":
				_, e = db.Exec(`UPDATE openai_excel_login_tasks SET created_at=NOW()-INTERVAL '31 minutes' WHERE id=$1`, queued.ID)
				require.NoError(t, e)
			}
			ok, e := repo.FinishExcelLogin(ctx, queued.ID, worker, creds, "encrypted-result", service.ExcelBPSGrantState(123))
			if scenario == "identity mismatch" {
				require.Error(t, e)
			} else {
				require.NoError(t, e)
			}
			require.Equal(t, scenario == "success", ok)
			grant, e := repo.GetExcelCredentials(ctx, id)
			require.NoError(t, e)
			switch scenario {
			case "success":
				require.Equal(t, "encrypted-result", grant)
			case "manual grant":
				require.Equal(t, "new-manual-grant", grant)
			default:
				require.Empty(t, grant)
			}
			var token string
			require.NoError(t, db.QueryRow(`SELECT credentials->>'access_token' FROM accounts WHERE id=$1`, id).Scan(&token))
			if scenario == "primary changed" {
				require.Equal(t, "new-primary", token)
			} else {
				require.Equal(t, "native-primary", token)
			}
			_, _ = repo.FailExcelLogin(ctx, queued.ID, "worker-a", "worker_failed")
		})
	}
	t.Run("concurrent queue and claim", func(t *testing.T) {
		_, err = db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES(99,$1,'{"openai_excel_bps":true}')`, string(primaryJSON))
		require.NoError(t, err)
		saved, e := repo.SaveExcelLoginConfig(ctx, &service.ExcelLoginConfig{AccountID: 99, Enabled: true, LoginEmail: "user@example.com", PasswordCiphertext: "encrypted"}, 0)
		require.NoError(t, e)
		require.True(t, saved)
		var wg sync.WaitGroup
		results := make(chan *service.ExcelLoginTask, 8)
		failures := make(chan error, 8)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task, e := repo.QueueExcelLogin(ctx, 99, false)
				results <- task
				failures <- e
			}()
		}
		wg.Wait()
		close(results)
		close(failures)
		n := 0
		for item := range results {
			if item != nil {
				n++
			}
		}
		for e := range failures {
			require.NoError(t, e)
		}
		require.Equal(t, 1, n)
		results = make(chan *service.ExcelLoginTask, 8)
		failures = make(chan error, 8)
		for i := range 8 {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				task, e := repo.ClaimExcelLogin(ctx, fmt.Sprintf("worker-%d", i))
				results <- task
				failures <- e
			}(i)
		}
		wg.Wait()
		close(results)
		close(failures)
		n = 0
		for item := range results {
			if item != nil {
				n++
			}
		}
		for e := range failures {
			require.NoError(t, e)
		}
		require.Equal(t, 1, n)
	})
	t.Run("missing route preparation and ready reconciliation", func(t *testing.T) {
		_, e := db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES(110,$1,'{"openai_excel_bps":true}')`, string(primaryJSON))
		require.NoError(t, e)
		require.NoError(t, repo.PrepareMissingExcelRoutes(ctx))
		var pending bool
		require.NoError(t, db.QueryRow(`SELECT COALESCE(extra->>'openai_excel_bps_authorization_pending','false')::boolean FROM accounts WHERE id=110`).Scan(&pending))
		require.True(t, pending)
		_, e = db.Exec(`INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES(110,'ready')`)
		require.NoError(t, e)
		require.NoError(t, repo.PrepareMissingExcelRoutes(ctx))
		require.NoError(t, db.QueryRow(`SELECT COALESCE(extra->>'openai_excel_bps_authorization_pending','false')::boolean FROM accounts WHERE id=110`).Scan(&pending))
		require.False(t, pending)
	})
	t.Run("failed accounts do not starve queue", func(t *testing.T) {
		// More than one scan page of exhausted configurations must not hide later accounts.
		_, e := db.Exec(`INSERT INTO accounts(id,credentials,extra) SELECT n,$1::jsonb,'{"openai_excel_bps":true}'::jsonb FROM generate_series(200,260) n`, string(primaryJSON))
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO openai_excel_login_configs(account_id,enabled,login_email,password_ciphertext) SELECT n,true,'user@example.com','encrypted' FROM generate_series(200,260) n`)
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO openai_excel_login_tasks(account_id,config_revision,expected_credentials_hash,expected_grant_hash,status,updated_at) SELECT a.id,1,'','', 'failed',NOW()-INTERVAL '1 hour' FROM accounts a CROSS JOIN generate_series(1,3) WHERE a.id BETWEEN 200 AND 259`)
		require.NoError(t, e)
		ids, e := repo.ListMissingExcelLogins(ctx)
		require.NoError(t, e)
		require.Contains(t, ids, int64(260))
		require.NotContains(t, ids, int64(200))
	})
	t.Run("restart resumes explicit reauthorization with existing grant", func(t *testing.T) {
		_, e := db.Exec(`UPDATE openai_excel_login_tasks SET status='cancelled' WHERE status IN ('queued','running')`)
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES(300,$1,'{}')`, string(primaryJSON))
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES(300,'old-grant')`)
		require.NoError(t, e)
		saved, e := repo.SaveExcelLoginConfig(ctx, &service.ExcelLoginConfig{AccountID: 300, Enabled: true, LoginEmail: "user@example.com", PasswordCiphertext: "encrypted"}, 0)
		require.NoError(t, e)
		require.True(t, saved)
		task, e := repo.QueueExcelLogin(ctx, 300, true)
		require.NoError(t, e)
		require.NotNil(t, task)
		active, e := repo.HasActiveExcelLogins(ctx)
		require.NoError(t, e)
		require.True(t, active)
		_, e = db.Exec(`UPDATE openai_excel_login_tasks SET created_at=NOW()-INTERVAL '31 minutes' WHERE id=$1`, task.ID)
		require.NoError(t, e)
		active, e = repo.HasActiveExcelLogins(ctx)
		require.NoError(t, e)
		require.False(t, active)
	})

}
