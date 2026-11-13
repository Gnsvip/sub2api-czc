//go:build unit

package repository

import (
	"context"
	"database/sql"
	"errors"
	"testing"

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
