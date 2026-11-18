package repository

import (
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDeleteStalePelicanTestsSQLKeepsTwentyFourHours(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("intelligent_test_public.go")
	require.NoError(t, err)
	body := string(src)
	require.Contains(t, body, "INTERVAL '24 hours'")
	require.Contains(t, body, "DELETE FROM account_tests")
	require.NotContains(t, body, "INTERVAL '6 hours'")
	require.NotContains(t, body, "INTERVAL '3 hours'")
}

func TestIntelligentAccountWhereSchedulableMeansDispatchable(t *testing.T) {
	t.Parallel()
	w := intelligentAccountWhere(service.IntelligentTestFilter{AccountStatus: "schedulable"})
	sqlText := w.sql()
	require.Contains(t, sqlText, "a.status = 'active'")
	require.Contains(t, sqlText, "a.schedulable = TRUE")
	require.Contains(t, sqlText, "a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW()")
	require.Contains(t, sqlText, "a.overload_until IS NULL OR a.overload_until <= NOW()")
	require.Contains(t, sqlText, "a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW()")
	require.Contains(t, sqlText, "a.auto_pause_on_expired = FALSE")
	require.Empty(t, w.args)

	exact := intelligentAccountWhere(service.IntelligentTestFilter{AccountStatus: "disabled"})
	require.Contains(t, exact.sql(), "a.status=$1")
	require.Equal(t, []any{"disabled"}, exact.args)
}

func TestListPelicanCandidatesSQLRequiresSchedulableAndPrefers292(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("intelligent_test_public.go")
	require.NoError(t, err)
	body := string(src)
	require.Contains(t, body, "func (r *intelligentTestRepository) ListPelicanCandidates")
	require.Contains(t, body, "rate_limit_reset_at")
	require.Contains(t, body, "temp_unschedulable_until")
	require.Contains(t, body, "codex_7d_used_percent")
	require.Contains(t, body, "codex_turn_ticket:gpt-6-astra")
	require.Contains(t, body, "codex_tibo_verdict:http")
	require.Contains(t, body, "AS has_healthy_http")
	require.Contains(t, body, "AS has_ticket")
	require.Contains(t, body, "COALESCE((a.extra->$5->>'length')::int, 0) = 292")
	require.Contains(t, body, "a.type = ANY($3)")
	require.NotContains(t, body, "https://ai8.my/v1")
	require.NotContains(t, body, "AS preferred")
}

func TestListPelicanSlotAttemptsSQLIncludesRetries(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("intelligent_test_public.go")
	require.NoError(t, err)
	body := string(src)
	require.Contains(t, body, "ListPelicanSlotAttempts")
	require.Contains(t, body, "request_key LIKE $1 || '-r%'")
	require.Contains(t, body, "t.result ILIKE '%<svg%'")
}

func TestAdminIntelligentTestSQLExcludesPelicanSchedule(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("intelligent_test_repo.go")
	require.NoError(t, err)
	body := string(src)
	require.Contains(t, body, "pelican-schedule")
	require.Contains(t, body, "intelligentAdminManualSQL")
	require.Contains(t, body, "pelican-slot-%")
	require.GreaterOrEqual(t, strings.Count(body, "intelligentAdminManualSQL"), 6)
}

func TestIntelligentClaimPickSQLStartsImmediately(t *testing.T) {
	t.Parallel()
	require.NotContains(t, intelligentClaimPickSQL, "<4")
	require.NotContains(t, intelligentClaimPickSQL, "status='running'")
	require.False(t, strings.Contains(strings.ToLower(intelligentClaimPickSQL), "not exists"))
	require.Contains(t, intelligentClaimPickSQL, "status='queued'")
}

func TestIntelligentTestEnqueueSnapshotsSavedModel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		model     string
		override  string
		source    string
		wantModel string
	}{
		{"scheduled astra 6.2", "gpt-6.2-astra", "", service.IntelligentTestSourcePelicanSchedule, "gpt-6.2-astra"},
		{"scheduled sol 6.2", "gpt-6.2-sol", "", service.IntelligentTestSourcePelicanSchedule, "gpt-6.2-sol"},
		{"empty selection defers to runner", "", "", service.IntelligentTestSourcePelicanSchedule, ""},
		{"manual selection still overrides", "gpt-6.2-astra", "gpt-6.2-sol", "", "gpt-6.2-sol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			cfg := service.IntelligentTestConfig{
				Model: tc.model, Prompt: "saved prompt", Evaluator: "svg_structure",
				ScheduleEnabled: true, GroupID: 42, TimeoutSeconds: 300,
				VisualJudgeEnabled: true, VisualPassScore: 50,
			}
			saved, err := json.Marshal(cfg)
			require.NoError(t, err)
			req := service.IntelligentTestEnqueue{
				AccountIDs: []int64{11}, TestTypes: []string{"pelican"},
				Prompts:        map[string]string{"pelican": "animal animation"},
				IdempotencyKey: "model-regression", Source: tc.source, ScheduledGroupID: 42,
			}
			if tc.override != "" {
				req.Models = map[string]string{"pelican": tc.override}
			}
			cfg.Model = tc.wantModel
			cfg.Prompt = "animal animation"
			cfg.Source = tc.source
			snapshot, err := json.Marshal(cfg)
			require.NoError(t, err)

			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT fingerprint,record_ids FROM intelligent_test_requests`).
				WithArgs(int64(3), req.IdempotencyKey).WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM account_tests`).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			mock.ExpectQuery(`SELECT enabled,config FROM test_settings`).WithArgs("pelican").
				WillReturnRows(sqlmock.NewRows([]string{"enabled", "config"}).AddRow(true, saved))
			mock.ExpectQuery(`SELECT a.id FROM accounts`).WithArgs(int64(11)).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(11))
			if tc.source == service.IntelligentTestSourcePelicanSchedule {
				mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM account_groups`).WithArgs(int64(11), int64(42)).
					WillReturnRows(sqlmock.NewRows([]string{"member"}).AddRow(true))
			}
			mock.ExpectQuery(`SELECT .* FROM account_tests t WHERE account_id=`).WithArgs(int64(11), "pelican").
				WillReturnError(sql.ErrNoRows)
			// Check both the model column and the full saved configuration snapshot.
			mock.ExpectQuery(`INSERT INTO account_tests`).
				WithArgs(int64(11), "pelican", cfg.Prompt, tc.wantModel, false, string(snapshot), int64(3)).
				WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(17))
			now := time.Now()
			columns := strings.Split("id,account_id,test_type,status,score,result,result_image,input,raw_response,raw_truncated,error_message,duration_ms,model,anti_degradation,config_snapshot,evaluation,started_at,finished_at,created_at,lease_token,queue_reason,available_at", ",")
			mock.ExpectQuery(`SELECT .* FROM account_tests t WHERE id=`).WithArgs(int64(17)).
				WillReturnRows(sqlmock.NewRows(columns).AddRow(17, 11, "pelican", "queued", nil, "", "", cfg.Prompt, "", false, "", 0, tc.wantModel, false, snapshot, []byte(`{}`), nil, nil, now, "", "", now))
			mock.ExpectExec(`INSERT INTO intelligent_test_requests`).
				WithArgs(int64(3), req.IdempotencyKey, sqlmock.AnyArg(), sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`INSERT INTO audit_logs`).
				WithArgs(int64(3), "admin.intelligent_tests.enqueue", sqlmock.AnyArg()).
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			out, err := NewIntelligentTestRepository(db).Enqueue(t.Context(), 3, req)
			require.NoError(t, err)
			require.Equal(t, 1, out.CreatedCount)
			require.Len(t, out.Records, 1)
			require.Equal(t, tc.wantModel, out.Records[0].Model)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
