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
	_, err = db.Exec(`CREATE TABLE accounts(id BIGINT PRIMARY KEY,platform TEXT DEFAULT 'openai',type TEXT DEFAULT 'oauth',status TEXT DEFAULT 'active',error_message TEXT,schedulable BOOLEAN DEFAULT TRUE,proxy_id BIGINT,credentials JSONB,extra JSONB,deleted_at TIMESTAMPTZ,updated_at TIMESTAMPTZ DEFAULT NOW()); CREATE TABLE scheduler_outbox(id BIGSERIAL PRIMARY KEY,event_type TEXT,account_id BIGINT,group_id BIGINT,payload JSONB,dedup_key TEXT); CREATE UNIQUE INDEX dedup ON scheduler_outbox(dedup_key) WHERE dedup_key IS NOT NULL`)
	require.NoError(t, err)
	for _, migration := range []string{"276_excel_bps_independent_grant.sql", "277_excel_bps_authorization_queue.sql", "278_excel_bps_auto_policy.sql", "282_codex_automatic_authorization.sql", "283_codex_preferred_workspace.sql", "284_codex_browser_diagnostic.sql"} {
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

	t.Run("automatic policy persists without touching credentials or groups", func(t *testing.T) {
		now := time.Now().Add(-time.Hour)
		config := map[string]any{service.ExcelBPSAutoPolicyKey: map[string]any{"enabled": true, "auto_restore": true, "bps": map[string]any{"failure_threshold": 1, "pass_threshold": 1, "all_models": true}}, "keep": "value"}
		raw, e := json.Marshal(config)
		require.NoError(t, e)
		_, e = db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES(400,$1,$2::jsonb)`, string(primaryJSON), string(raw))
		require.NoError(t, e)
		require.NoError(t, repo.ApplyExcelBPSAutoPolicy(ctx, 400, now))
		config[service.OpenAIAstraPolicyExtraKey] = map[string]any{"model": "gpt-6-astra", "recent_samples": []map[string]any{{"verdict": "degraded", "at": now.Add(time.Second).Format(time.RFC3339Nano)}}}
		raw, e = json.Marshal(config)
		require.NoError(t, e)
		_, e = db.Exec(`UPDATE accounts SET extra=$1::jsonb WHERE id=400`, string(raw))
		require.NoError(t, e)
		require.NoError(t, repo.ApplyExcelBPSAutoPolicy(ctx, 400, now.Add(2*time.Second)))
		var enabled, pending, token, keep string
		require.NoError(t, db.QueryRow(`SELECT extra->>'openai_excel_bps',extra->>'openai_excel_bps_authorization_pending',credentials->>'access_token',extra->>'keep' FROM accounts WHERE id=400`).Scan(&enabled, &pending, &token, &keep))
		require.Equal(t, "true", enabled)
		require.Equal(t, "true", pending)
		require.Equal(t, "native-primary", token)
		require.Equal(t, "value", keep)
	})

	t.Run("partial and bulk activation prepares pending atomically", func(t *testing.T) {
		for i, scenario := range []string{"missing", "ready", "disabled", "legacy"} {
			id := int64(500 + i)
			native := string(primaryJSON)
			if scenario == "legacy" {
				native = `{"client_id":"app_fnr0pYvVwwFDocDumLG3H2Bp"}`
			}
			_, e := db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES($1,$2::jsonb,'{"keep":"value","openai_excel_bps_authorization_pending":true}')`, id, native)
			require.NoError(t, e)
			if scenario == "ready" {
				_, e = db.Exec(`INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,'grant')`, id)
				require.NoError(t, e)
			}
			payload := `{"openai_excel_bps":true}`
			if scenario == "disabled" {
				payload = `{"openai_excel_bps":false}`
			}
			_, e = db.Exec(`UPDATE accounts SET extra=`+excelBPSPendingExtraSQL("COALESCE(extra,'{}'::jsonb)||$1::jsonb")+` WHERE id=$2`, payload, id)
			require.NoError(t, e)
			var pending bool
			var keep string
			require.NoError(t, db.QueryRow(`SELECT COALESCE(extra->>'openai_excel_bps_authorization_pending','false')::boolean,extra->>'keep' FROM accounts WHERE id=$1`, id).Scan(&pending, &keep))
			require.Equal(t, scenario == "missing", pending, scenario)
			require.Equal(t, "value", keep)
		}
	})

	t.Run("Codex queue credential and administrator guards", func(t *testing.T) {
		_, e := db.Exec(`UPDATE openai_excel_login_tasks SET status='cancelled' WHERE status IN ('queued','running')`)
		require.NoError(t, e)
		codexCandidate := map[string]any{"client_id": openai.ClientID, "refresh_token": "codex-refresh", "email": "user@example.com", "chatgpt_account_id": "workspace", "chatgpt_user_id": "user"}
		auth := map[string]any{"chatgpt_account_id": "workspace", "chatgpt_user_id": "user"}
		codexCandidate["access_token"] = jwt(map[string]any{"client_id": openai.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": auth})
		codexCandidate["id_token"] = jwt(map[string]any{"aud": []string{openai.ClientID}, "exp": time.Now().Add(time.Hour).Unix(), "email": "user@example.com", "https://api.openai.com/auth": auth})
		healthyAstra := func() *service.CodexLoginAstraResult {
			return &service.CodexLoginAstraResult{Verdict: "healthy", SampledAt: time.Now()}
		}
		newAccount := func(id int64, enabled bool, pending bool) *service.ExcelLoginConfig {
			t.Helper()
			message := "Unauthorized (401): expired credentials"
			if pending {
				message = service.CodexAuthorizationPending
			}
			_, err := db.Exec(`INSERT INTO accounts(id,credentials,extra,status,error_message,schedulable,proxy_id) VALUES($1,$2::jsonb,jsonb_build_object('keep','custom','openai_codex_authorization_pending',$3::boolean),'error',$4,FALSE,5)`, id, string(primaryJSON), pending, message)
			require.NoError(t, err)
			config := &service.ExcelLoginConfig{AccountID: id, Enabled: enabled, LoginEmail: "user@example.com", PasswordCiphertext: "encrypted-password", PreferredWorkspaceID: "workspace"}
			ok, err := repo.SaveCodexLoginConfig(ctx, config, 0)
			require.NoError(t, err)
			require.True(t, ok)
			saved, err := repo.GetCodexLoginConfig(ctx, id)
			require.NoError(t, err)
			require.Equal(t, "workspace", saved.PreferredWorkspaceID)
			return config
		}
		for i, scenario := range []string{"initial success", "paused recovery", "other worker", "new credentials", "new config", "new workspace preference", "wrong first workspace", "admin pause", "proxy change", "disabled", "expired task", "first authorization without pending flag"} {
			t.Run(scenario, func(t *testing.T) {
				id := int64(1000 + i)
				config := newAccount(id, false, scenario == "initial success")
				if scenario == "first authorization without pending flag" || scenario == "wrong first workspace" {
					_, e = db.Exec(`UPDATE accounts SET credentials='{"email":"user@example.com"}'::jsonb,status='active',error_message=NULL WHERE id=$1`, id)
					require.NoError(t, e)
				}
				if scenario == "admin pause" {
					_, e = db.Exec(`UPDATE accounts SET schedulable=TRUE WHERE id=$1`, id)
					require.NoError(t, e)
				}
				auto, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.Nil(t, auto, "disabled auto recovery must not queue")
				queued, e := repo.QueueCodexLogin(ctx, id, true)
				require.NoError(t, e)
				require.NotNil(t, queued)
				defer func() {
					_, _ = db.Exec(`UPDATE openai_excel_login_tasks SET status='cancelled' WHERE account_id=$1 AND status IN ('queued','running')`, id)
				}()
				excel, e := repo.ClaimExcelLogin(ctx, "excel-worker")
				require.NoError(t, e)
				require.Nil(t, excel, "Excel must not claim Codex tasks")
				claimed, e := repo.ClaimCodexLogin(ctx, "worker-a")
				require.NoError(t, e)
				require.NotNil(t, claimed)
				require.Equal(t, queued.ID, claimed.ID)
				_, e = db.Exec(`INSERT INTO openai_excel_oauth_credentials(account_id,credentials_ciphertext) VALUES($1,'preserve-excel')`, id)
				require.NoError(t, e)
				worker := "worker-a"
				switch scenario {
				case "other worker":
					worker = "worker-b"
				case "new credentials":
					_, e = db.Exec(`UPDATE accounts SET credentials=credentials||'{"access_token":"new-admin-token"}'::jsonb WHERE id=$1`, id)
				case "new workspace preference":
					config.PreferredWorkspaceID = "different-workspace"
					_, e = repo.SaveCodexLoginConfig(ctx, config, 1)
				case "wrong first workspace":
					_, e = db.Exec(`UPDATE openai_codex_login_configs SET preferred_workspace_id='different-workspace' WHERE account_id=$1`, id)
				case "new config":
					_, e = repo.SaveCodexLoginConfig(ctx, config, 1)
				case "admin pause":
					_, e = db.Exec(`UPDATE accounts SET schedulable=FALSE WHERE id=$1`, id)
				case "proxy change":
					_, e = db.Exec(`UPDATE accounts SET proxy_id=6 WHERE id=$1`, id)
				case "disabled":
					_, e = db.Exec(`UPDATE accounts SET status='disabled' WHERE id=$1`, id)
				case "expired task":
					_, e = db.Exec(`UPDATE openai_excel_login_tasks SET created_at=NOW()-INTERVAL '31 minutes' WHERE id=$1`, queued.ID)
				}
				require.NoError(t, e)
				ok, e := repo.FinishCodexLogin(ctx, queued.ID, worker, codexCandidate, healthyAstra())
				require.NoError(t, e)
				success := scenario == "initial success" || scenario == "paused recovery" || scenario == "first authorization without pending flag"
				require.Equal(t, success, ok)
				var reauthorizations int
				require.NoError(t, db.QueryRow(`SELECT COALESCE((extra->>'openai_codex_reauthorization_count')::int,0) FROM accounts WHERE id=$1`, id).Scan(&reauthorizations))
				if scenario == "paused recovery" {
					require.Equal(t, 1, reauthorizations)
				} else {
					require.Zero(t, reauthorizations, "initial, expired or rejected tasks must not count as reauthorizations")
				}
				var token, status, keep string
				var schedulable bool
				require.NoError(t, db.QueryRow(`SELECT COALESCE(credentials->>'access_token',''),status,schedulable,extra->>'keep' FROM accounts WHERE id=$1`, id).Scan(&token, &status, &schedulable, &keep))
				require.Equal(t, "custom", keep)
				if success {
					require.Equal(t, codexCandidate["access_token"], token)
					require.Equal(t, "active", status)
					require.Equal(t, scenario == "initial success", schedulable)
				} else if scenario == "wrong first workspace" {
					require.Empty(t, token, "a rejected first workspace must not write credentials")
				} else if scenario == "new credentials" {
					require.Equal(t, "new-admin-token", token)
				} else {
					require.Equal(t, "native-primary", token)
				}
				grant, e := repo.GetExcelCredentials(ctx, id)
				require.NoError(t, e)
				require.Equal(t, "preserve-excel", grant)
			})
		}
		t.Run("fresh Astra result gates recovery and retains successful authorization", func(t *testing.T) {
			for i, tc := range []struct {
				name, verdict   string
				paused, missing bool
			}{
				{"healthy resumes", "healthy", false, false},
				{"degraded stays paused", "degraded", false, false},
				{"unknown stays paused despite old healthy point", "unknown", false, false},
				{"missing result fails closed", "unknown", false, true},
				{"healthy preserves administrator pause", "healthy", true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					id := int64(1300 + i)
					newAccount(id, true, false)
					_, e := db.Exec(`UPDATE accounts SET schedulable=$2,
 extra=extra||jsonb_build_object('codex_tibo_verdict:http',jsonb_build_object('model','gpt-6-astra','recent_samples',
 jsonb_build_array(jsonb_build_object('verdict','healthy','at',$3::text)))) WHERE id=$1`, id, !tc.paused, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
					require.NoError(t, e)
					queued, e := repo.QueueCodexLogin(ctx, id, false)
					require.NoError(t, e)
					require.NotNil(t, queued)
					claimed, e := repo.ClaimCodexLogin(ctx, "astra-gate")
					require.NoError(t, e)
					require.Equal(t, queued.ID, claimed.ID)
					var astra *service.CodexLoginAstraResult
					if !tc.missing {
						astra = &service.CodexLoginAstraResult{Verdict: tc.verdict, SampledAt: time.Now()}
					}
					ok, e := repo.FinishCodexLogin(ctx, queued.ID, "astra-gate", codexCandidate, astra)
					require.NoError(t, e)
					require.True(t, ok)
					var status, token string
					var schedulable bool
					var count, points int
					require.NoError(t, db.QueryRow(`SELECT status,schedulable,credentials->>'access_token',
 (extra->>'openai_codex_reauthorization_count')::int,jsonb_array_length(extra->'codex_tibo_verdict:http'->'recent_samples')
 FROM accounts WHERE id=$1`, id).Scan(&status, &schedulable, &token, &count, &points))
					require.Equal(t, service.StatusActive, status, "valid authorization is retained independently of quality")
					require.Equal(t, tc.verdict == "healthy" && !tc.paused, schedulable)
					require.Equal(t, codexCandidate["access_token"], token)
					require.Equal(t, 1, count)
					if tc.verdict == "unknown" {
						require.Equal(t, 1, points, "unknown must not fabricate a definitive point")
					} else {
						require.Equal(t, 2, points, "preserve history and append the fresh result")
					}
					finished, e := repo.GetCodexLoginTask(ctx, id)
					require.NoError(t, e)
					require.Equal(t, "succeeded", finished.Status)
					stage := "succeeded"
					if tc.verdict != "healthy" {
						stage += "_astra_" + tc.verdict
					}
					require.Equal(t, stage, finished.Stage)
					ids, e := repo.ListCodexLoginCandidates(ctx)
					require.NoError(t, e)
					require.NotContains(t, ids, id, "failed quality must not trigger a fresh browser login")
				})
			}
		})
		t.Run("successful reauthorization counts once and survives task cleanup", func(t *testing.T) {
			const id int64 = 1190
			newAccount(id, true, false)
			_, e := db.Exec(`UPDATE accounts SET extra=extra||'{"openai_codex_reauthorization_count":"invalid"}'::jsonb WHERE id=$1`, id)
			require.NoError(t, e)
			for want := 1; want <= 2; want++ {
				_, e := db.Exec(`UPDATE accounts SET status='error',error_message='Authentication failed (401): expired credentials' WHERE id=$1`, id)
				require.NoError(t, e)
				queued, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.NotNil(t, queued)
				claimed, e := repo.ClaimCodexLogin(ctx, "count-worker")
				require.NoError(t, e)
				require.Equal(t, queued.ID, claimed.ID)
				type completion struct {
					changed bool
					err     error
				}
				results := make(chan completion, 2)
				for i := 0; i < 2; i++ {
					go func() {
						ok, err := repo.FinishCodexLogin(ctx, queued.ID, "count-worker", codexCandidate, healthyAstra())
						results <- completion{ok, err}
					}()
				}
				completed := 0
				for i := 0; i < 2; i++ {
					result := <-results
					require.NoError(t, result.err)
					if result.changed {
						completed++
					}
				}
				require.Equal(t, 1, completed)
				var count int
				var keep string
				require.NoError(t, db.QueryRow(`SELECT (extra->>'openai_codex_reauthorization_count')::int,extra->>'keep' FROM accounts WHERE id=$1`, id).Scan(&count, &keep))
				require.Equal(t, want, count)
				require.Equal(t, "custom", keep)
				_, e = db.Exec(`DELETE FROM openai_excel_login_tasks WHERE account_id=$1`, id)
				require.NoError(t, e)
			}
		})
		t.Run("administrator test authentication failures trigger guarded recovery", func(t *testing.T) {
			for i, tc := range []struct {
				message  string
				enabled  bool
				status   string
				expected bool
			}{
				{`Authentication failed (401): {"error":{"code":"token_revoked"},"status":401}`, true, service.StatusError, true},
				{"Authentication failed (401): expired credentials", true, service.StatusError, true},
				{"Authentication failed (401): account_deactivated", true, service.StatusError, false},
				{"Authentication failed (401): cyber_policy", true, service.StatusError, false},
				{"Authentication failed (403): permission denied", true, service.StatusError, false},
				{"untrusted text: Authentication failed (401): token_revoked", true, service.StatusError, false},
				{"Authentication failed (401): token_revoked", false, service.StatusError, false},
				{"Authentication failed (401): token_revoked", true, service.StatusDisabled, false},
				{"Authentication failed (401): token_revoked", true, service.StatusActive, false},
			} {
				id := int64(1130 + i)
				newAccount(id, tc.enabled, false)
				_, e := db.Exec(`UPDATE accounts SET status=$2,error_message=$3 WHERE id=$1`, id, tc.status, tc.message)
				require.NoError(t, e)
				expected := tc.expected
				ids, e := repo.ListCodexLoginCandidates(ctx)
				require.NoError(t, e)
				if expected {
					require.Contains(t, ids, id)
				} else {
					require.NotContains(t, ids, id)
				}
				queued, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				if !expected {
					require.Nil(t, queued)
					continue
				}
				require.NotNil(t, queued)
				claimed, e := repo.ClaimCodexLogin(ctx, "admin-test-recovery")
				require.NoError(t, e)
				require.Equal(t, queued.ID, claimed.ID)
				ok, e := repo.FinishCodexLogin(ctx, queued.ID, "admin-test-recovery", codexCandidate, healthyAstra())
				require.NoError(t, e)
				require.True(t, ok)
				var status string
				var schedulable bool
				var proxyID int64
				require.NoError(t, db.QueryRow(`SELECT status,schedulable,proxy_id FROM accounts WHERE id=$1`, id).Scan(&status, &schedulable, &proxyID))
				require.Equal(t, service.StatusActive, status)
				require.False(t, schedulable, "successful recovery must preserve an administrator pause")
				require.Equal(t, int64(5), proxyID)
			}
		})
		t.Run("personal fallback persists the new workspace and result without removing the pause", func(t *testing.T) {
			const id int64 = 1120
			newAccount(id, true, false)
			queued, e := repo.QueueCodexLogin(ctx, id, false)
			require.NoError(t, e)
			require.NotNil(t, queued)
			claimed, e := repo.ClaimCodexLogin(ctx, "personal-fallback")
			require.NoError(t, e)
			require.Equal(t, queued.ID, claimed.ID)
			personalAuth := map[string]any{"chatgpt_account_id": "personal", "chatgpt_user_id": "user", "chatgpt_plan_type": "free"}
			personal := map[string]any{"client_id": openai.ClientID, "refresh_token": "personal-refresh", "email": "user@example.com", "chatgpt_account_id": "personal", "chatgpt_user_id": "user", "plan_type": "free", service.CodexWorkspaceFallbackFrom: "workspace", service.CodexPersonalWorkspaceOnly: true}
			personal["access_token"] = jwt(map[string]any{"client_id": openai.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": personalAuth})
			personal["id_token"] = jwt(map[string]any{"aud": []string{openai.ClientID}, "exp": time.Now().Add(time.Hour).Unix(), "email": "user@example.com", "https://api.openai.com/auth": personalAuth})
			ok, e := repo.FinishCodexLogin(ctx, queued.ID, "personal-fallback", personal, healthyAstra())
			require.NoError(t, e)
			require.True(t, ok)
			var workspace, plan, status string
			var schedulable, leakedMarker bool
			var proxyID int64
			require.NoError(t, db.QueryRow(`SELECT credentials->>'chatgpt_account_id',credentials->>'plan_type',status,schedulable,proxy_id,credentials ? 'codex_workspace_fallback_from' OR credentials ? 'codex_personal_workspace_only' FROM accounts WHERE id=$1`, id).Scan(&workspace, &plan, &status, &schedulable, &proxyID, &leakedMarker))
			require.Equal(t, "personal", workspace)
			require.Equal(t, "free", plan)
			require.Equal(t, service.StatusActive, status)
			require.False(t, schedulable)
			require.EqualValues(t, 5, proxyID)
			require.False(t, leakedMarker)
			finished, e := repo.GetCodexLoginTask(ctx, id)
			require.NoError(t, e)
			require.Equal(t, "succeeded", finished.Status)
			require.Equal(t, "succeeded_personal_workspace_only", finished.Stage)
		})
		t.Run("initial attempt plus three retries after 60 seconds and success resets budget", func(t *testing.T) {
			const id int64 = 1100
			newAccount(id, true, false)
			for attempt := 0; attempt < 4; attempt++ {
				ids, e := repo.ListCodexLoginCandidates(ctx)
				require.NoError(t, e)
				require.Contains(t, ids, id, "initial attempt and all three retries must be discoverable")
				queued, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.NotNil(t, queued)
				claimed, e := repo.ClaimCodexLogin(ctx, "worker")
				require.NoError(t, e)
				require.Equal(t, queued.ID, claimed.ID)
				ok, e := repo.FailExcelLogin(ctx, queued.ID, "worker", "transport_error")
				require.NoError(t, e)
				require.True(t, ok)
				var count int
				require.NoError(t, db.QueryRow(`SELECT COALESCE((extra->>'openai_codex_reauthorization_count')::int,0) FROM accounts WHERE id=$1`, id).Scan(&count))
				require.Zero(t, count, "failed attempts must not increase the successful reauthorization count")
				// Still inside the cooldown, both the scanner and direct queue calls
				// must refuse another task. Age rows instead of sleeping in tests.
				_, e = db.Exec(`UPDATE openai_excel_login_tasks SET updated_at=NOW()-INTERVAL '30 seconds' WHERE id=$1`, queued.ID)
				require.NoError(t, e)
				ids, e = repo.ListCodexLoginCandidates(ctx)
				require.NoError(t, e)
				require.NotContains(t, ids, id)
				for _, manual := range []bool{false, true} {
					cooldown, e := repo.QueueCodexLogin(ctx, id, manual)
					require.NoError(t, e)
					require.Nil(t, cooldown, "automatic and manual attempts must respect the cooldown")
				}
				_, e = db.Exec(`UPDATE openai_excel_login_tasks SET updated_at=NOW()-INTERVAL '61 seconds' WHERE id=$1`, queued.ID)
				require.NoError(t, e)
			}
			ids, e := repo.ListCodexLoginCandidates(ctx)
			require.NoError(t, e)
			require.NotContains(t, ids, id)
			stopped, e := repo.QueueCodexLogin(ctx, id, false)
			require.NoError(t, e)
			require.Nil(t, stopped)
			manual, e := repo.QueueCodexLogin(ctx, id, true)
			require.NoError(t, e)
			require.NotNil(t, manual)
			_, e = repo.ClaimCodexLogin(ctx, "worker")
			require.NoError(t, e)
			ok, e := repo.FinishCodexLogin(ctx, manual.ID, "worker", codexCandidate, healthyAstra())
			require.NoError(t, e)
			require.True(t, ok)
			_, e = db.Exec(`UPDATE accounts SET status='error',error_message='Token revoked (401): expired' WHERE id=$1`, id)
			require.NoError(t, e)
			again, e := repo.QueueCodexLogin(ctx, id, false)
			require.NoError(t, e)
			require.NotNil(t, again)
			_, e = db.Exec(`UPDATE openai_excel_login_tasks SET status='cancelled' WHERE id=$1`, again.ID)
			require.NoError(t, e)
		})
		for index, reason := range []string{"password_rejected", "totp_rejected", "identity_mismatch", "workspace_unavailable", "workspace_selection_required", "security_check", "additional_verification", "account_blocked"} {
			t.Run(reason+" needs manual intervention", func(t *testing.T) {
				id := int64(1101 + index)
				newAccount(id, true, false)
				queued, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.NotNil(t, queued)
				_, e = repo.ClaimCodexLogin(ctx, "worker")
				require.NoError(t, e)
				diagnostic := "codex_browser_failed stage=protocol_connecting action=security_check reason=" + reason + " elapsed_ms=20000 action_ms=15000 http_status=403 response=document"
				ok, e := repo.FailExcelLogin(ctx, queued.ID, "another-worker", diagnostic)
				require.NoError(t, e)
				require.False(t, ok, "diagnostics must respect worker ownership")
				_, e = repo.FailExcelLogin(ctx, queued.ID, "worker", diagnostic)
				require.NoError(t, e)
				failed, e := repo.GetCodexLoginTask(ctx, id)
				require.NoError(t, e)
				require.Equal(t, reason, failed.ErrorCode)
				require.NotNil(t, failed.Diagnostic)
				require.Equal(t, 403, failed.Diagnostic.HTTPStatus)
				require.Equal(t, "security_check", failed.Diagnostic.Action)
				_, e = db.Exec(`UPDATE openai_excel_login_tasks SET updated_at=NOW()-INTERVAL '61 seconds' WHERE id=$1`, queued.ID)
				require.NoError(t, e)
				again, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.Nil(t, again)
				ids, e := repo.ListCodexLoginCandidates(ctx)
				require.NoError(t, e)
				require.NotContains(t, ids, id)
			})
		}
		for index, scenario := range []string{"initial attempt", "retry after cooldown"} {
			t.Run("concurrent queue only admits one task for "+scenario, func(t *testing.T) {
				id := int64(1200 + index)
				newAccount(id, true, false)
				if index > 0 {
					queued, e := repo.QueueCodexLogin(ctx, id, false)
					require.NoError(t, e)
					require.NotNil(t, queued)
					claimed, e := repo.ClaimCodexLogin(ctx, "retry-worker")
					require.NoError(t, e)
					require.NotNil(t, claimed)
					require.Equal(t, queued.ID, claimed.ID)
					ok, e := repo.FailExcelLogin(ctx, queued.ID, "retry-worker", "transport_error")
					require.NoError(t, e)
					require.True(t, ok)
					_, e = db.Exec(`UPDATE openai_excel_login_tasks SET updated_at=NOW()-INTERVAL '61 seconds' WHERE id=$1`, queued.ID)
					require.NoError(t, e)
				}
				type result struct {
					task *service.ExcelLoginTask
					err  error
				}
				results := make(chan result, 8)
				for i := 0; i < 8; i++ {
					go func() { task, e := repo.QueueCodexLogin(ctx, id, false); results <- result{task, e} }()
				}
				count := 0
				for i := 0; i < 8; i++ {
					r := <-results
					require.NoError(t, r.err)
					if r.task != nil {
						count++
					}
				}
				require.Equal(t, 1, count)
				_, e := db.Exec(`UPDATE openai_excel_login_tasks SET status='cancelled' WHERE account_id=$1`, id)
				require.NoError(t, e)
			})
		}
		t.Run("deleted accounts purge login secrets atomically while disabled accounts retain them", func(t *testing.T) {
			seed := func(id int64) *service.ExcelLoginTask {
				t.Helper()
				newAccount(id, true, false)
				_, e := db.Exec(`INSERT INTO openai_excel_login_configs(account_id,enabled,login_email,password_ciphertext,totp_ciphertext) VALUES($1,true,'user@example.com','encrypted-password','encrypted-totp')`, id)
				require.NoError(t, e)
				_, e = db.Exec(`UPDATE openai_codex_login_configs SET totp_ciphertext='encrypted-totp' WHERE account_id=$1`, id)
				require.NoError(t, e)
				queued, e := repo.QueueCodexLogin(ctx, id, false)
				require.NoError(t, e)
				require.NotNil(t, queued)
				claimed, e := repo.ClaimCodexLogin(ctx, "cleanup-worker")
				require.NoError(t, e)
				require.NotNil(t, claimed)
				require.Equal(t, queued.ID, claimed.ID)
				return queued
			}
			assertClean := func(task *service.ExcelLoginTask) {
				t.Helper()
				var count int
				require.NoError(t, db.QueryRow(`SELECT (SELECT count(*) FROM openai_codex_login_configs WHERE account_id=$1)+(SELECT count(*) FROM openai_excel_login_configs WHERE account_id=$1)`, task.AccountID).Scan(&count))
				require.Zero(t, count, "both profiles must remove saved password/TOTP data")
				stored, e := repo.GetLoginTaskByID(ctx, task.ID)
				require.NoError(t, e)
				require.Equal(t, "cancelled", stored.Status)
				require.Equal(t, "account_changed", stored.ErrorCode)
				queued, e := repo.QueueCodexLogin(ctx, task.AccountID, true)
				require.NoError(t, e)
				require.Nil(t, queued)
				ok, e := repo.FinishCodexLogin(ctx, task.ID, "cleanup-worker", codexCandidate, healthyAstra())
				require.NoError(t, e)
				require.False(t, ok, "a stale callback cannot revive a deleted account")
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM accounts WHERE id=$1 AND deleted_at IS NOT NULL`, task.AccountID).Scan(&count))
				require.Equal(t, 1, count, "keep the account row referenced by usage history")
			}
			legacy := seed(4000)
			live := seed(4001)
			disabled := seed(4002)
			_, e := db.Exec(`UPDATE accounts SET deleted_at=NOW() WHERE id=4000; UPDATE accounts SET status='disabled' WHERE id=4002`)
			require.NoError(t, e)
			_, e = db.Exec(`INSERT INTO openai_excel_login_tasks(account_id,config_revision,expected_credentials_hash,expected_grant_hash,oauth_profile,status,stage) VALUES(4000,1,'history','','codex','succeeded','succeeded')`)
			require.NoError(t, e)
			raw, e := os.ReadFile("../../migrations/285_account_delete_login_cleanup.sql")
			require.NoError(t, e)
			_, e = db.Exec(string(raw))
			require.NoError(t, e)
			assertClean(legacy)
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM openai_excel_login_tasks WHERE account_id=4000 AND status='succeeded'`).Scan(&count))
			require.Equal(t, 1, count, "completed diagnostic history is preserved")
			for _, id := range []int64{4001, 4002} {
				require.NoError(t, db.QueryRow(`SELECT (SELECT count(*) FROM openai_codex_login_configs WHERE account_id=$1)+(SELECT count(*) FROM openai_excel_login_configs WHERE account_id=$1)`, id).Scan(&count))
				require.Equal(t, 2, count, "active and disabled accounts retain both configurations")
			}
			tx, e := db.BeginTx(ctx, nil)
			require.NoError(t, e)
			defer tx.Rollback()
			_, e = tx.Exec(`UPDATE accounts SET deleted_at=NOW() WHERE id=4001`)
			require.NoError(t, e)
			require.NoError(t, tx.QueryRow(`SELECT count(*) FROM openai_codex_login_configs WHERE account_id=4001`).Scan(&count))
			require.Zero(t, count)
			require.NoError(t, tx.Rollback())
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM openai_codex_login_configs WHERE account_id=4001`).Scan(&count))
			require.Equal(t, 1, count, "rollback must restore configuration too")
			current, e := repo.GetLoginTaskByID(ctx, live.ID)
			require.NoError(t, e)
			require.Equal(t, "running", current.Status)
			_, e = db.Exec(`UPDATE accounts SET deleted_at=NOW() WHERE id IN (4001,4002)`)
			require.NoError(t, e)
			assertClean(live)
			assertClean(disabled)
			// Adding the same email later creates a new identity without inheriting
			// deleted credentials. Saving fresh configuration still works normally.
			_, e = db.Exec(`INSERT INTO accounts(id,credentials,extra) VALUES(4003,'{"email":"user@example.com"}','{}')`)
			require.NoError(t, e)
			config, e := repo.GetCodexLoginConfig(ctx, 4003)
			require.NoError(t, e)
			require.Nil(t, config)
			ok, e := repo.SaveCodexLoginConfig(ctx, &service.ExcelLoginConfig{AccountID: 4003, Enabled: true, LoginEmail: "user@example.com", PasswordCiphertext: "fresh-password"}, 0)
			require.NoError(t, e)
			require.True(t, ok)
			_, e = db.Exec(string(raw))
			require.NoError(t, e, "cleanup can be retried without changing live settings")
			config, e = repo.GetCodexLoginConfig(ctx, 4003)
			require.NoError(t, e)
			require.Equal(t, "fresh-password", config.PasswordCiphertext)
		})
	})

}
