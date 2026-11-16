package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserPelicanTestsDistinguishesIncompleteAndRestrictedPreviews(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now()
	rows := sqlmock.NewRows([]string{"id", "status", "result", "model", "created_at", "finished_at", "duration_ms", "group_name", "reasoning_effort", "prompt", "evaluation"})
	rows.AddRow(4, "failed", `<html><body><svg><path d="`, "gpt-6-astra", now, now, 90053, "GPT-PRO", "low", "draw", `{"evaluator_version":3,"visual_judgement":{"state":"passed","score":85,"threshold":70,"reason":"OK"}}`)
	rows.AddRow(3, "completed", `<html><body><svg></svg>`, "gpt-6-astra", now, now, 120000, "GPT-PRO", "low", "draw", `{"evaluator_version":3,"visual_judgement":{"state":"passed","score":85,"threshold":70,"reason":"OK"}}`)
	rows.AddRow(2, "completed", `<html><body><svg></svg><script>draw()</script></body></html>`, "gpt-6-astra", now, now, 120000, "GPT-PRO", "low", "draw", `{"evaluator_version":3,"visual_judgement":{"state":"passed","score":85,"threshold":70,"reason":"OK"}}`)
	rows.AddRow(1, "completed", `<html><body><svg></svg></body></html>`, "gpt-6-astra", now, now, 120000, "GPT-PRO", "low", "draw", `{"evaluator_version":3,"visual_judgement":{"state":"passed","score":85,"threshold":70,"reason":"OK"}}`)
	mock.ExpectQuery(`SELECT COUNT\(\*\)`).WithArgs(int64(42)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectQuery(`SELECT t.id,t.status,t.result`).WithArgs(int64(42), 96, 0).WillReturnRows(rows)

	page, err := (&intelligentTestRepository{db: db}).UserPelicanTests(context.Background(), service.IntelligentTestFilter{Page: 1, PageSize: 96, GroupID: 42})
	require.NoError(t, err)
	require.EqualValues(t, 4, page.Total)
	require.Len(t, page.Items, 4, "keep interrupted attempts visible in history")
	for _, item := range page.Items[:2] {
		require.Empty(t, item.HTML)
		require.Equal(t, "incomplete", item.PreviewIssue)
	}
	require.Equal(t, "scripts_removed", page.Items[2].PreviewIssue)
	require.Contains(t, page.Items[2].HTML, "<svg>")
	require.NotContains(t, page.Items[2].HTML, "<script>")
	require.Empty(t, page.Items[3].PreviewIssue)
	require.NotNil(t, page.Items[3].VisualJudgement)
	require.Equal(t, "passed", page.Items[3].VisualJudgement.State)
	require.Equal(t, 85.0, *page.Items[3].VisualJudgement.Score)
	require.Contains(t, page.Items[3].HTML, "</html>")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserPelicanSummaryAndDetailKeepAccessConstraints(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "detail"}[detail], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			// Both queries must keep every authorization/retention predicate; detail
			// must add the ID predicate rather than falling back to the admin Get.
			constraints := `(?s)s.enabled AND s.user_visible.*t.config_snapshot->>'group_id'=s.config->>'group_id'.*a.deleted_at IS NULL.*INTERVAL '24 hours'.*EXISTS.*g.deleted_at IS NULL AND g.status='active' AND g.id=\$1`
			filter := service.IntelligentTestFilter{Page: 1, PageSize: 33, GroupID: 42, PelicanSummary: !detail}
			now := time.Now()
			rows := sqlmock.NewRows([]string{"id", "status", "result", "model", "created_at", "finished_at", "duration_ms", "group_name", "reasoning_effort", "prompt", "evaluation"}).AddRow(17, "completed", `<html><body><svg></svg></body></html>`, "model", now, now, 10, "group", "low", "draw", `{}`)
			if detail {
				filter.PelicanID = 17
				constraints += `.*AND t.id=\$2`
				mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*`+constraints).WithArgs(int64(42), int64(17)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(`(?s)SELECT t.id.*`+constraints+`.*LIMIT \$3 OFFSET \$4`).WithArgs(int64(42), int64(17), 33, 0).WillReturnRows(rows)
			} else {
				mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*` + constraints).WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
				mock.ExpectQuery(`(?s)SELECT t.id.*`+constraints+`.*LIMIT \$2 OFFSET \$3`).WithArgs(int64(42), 33, 0).WillReturnRows(rows)
			}
			page, err := (&intelligentTestRepository{db: db}).UserPelicanTests(context.Background(), filter)
			require.NoError(t, err)
			require.Len(t, page.Items, 1)
			require.True(t, page.Items[0].HasHTML)
			if detail {
				require.Contains(t, page.Items[0].HTML, "<svg>")
			} else {
				require.Empty(t, page.Items[0].HTML)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
