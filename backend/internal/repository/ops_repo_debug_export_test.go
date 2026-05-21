package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestOpsRepositorySampleSystemLogsForDebugExportUsesBoundedQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)

	rows := sqlmock.NewRows([]string{
		"id",
		"created_at",
		"level",
		"component",
		"message",
		"request_id",
		"client_request_id",
		"platform",
		"model",
	}).
		AddRow(int64(9), end.Add(-time.Minute), "error", "handler.gateway.messages", "first", "req-1", "client-1", "openai", "gpt-4o").
		AddRow(int64(8), end.Add(-2*time.Minute), "warn", "handler.gateway.messages", "second", "req-2", "client-2", "openai", "gpt-4o-mini")

	mock.ExpectQuery(`(?s)FROM ops_system_logs l\s+WHERE l\.created_at >= \$1 AND l\.created_at < \$2\s+ORDER BY l\.created_at DESC, l\.id DESC\s+LIMIT \$3`).
		WithArgs(start, end, 2).
		WillReturnRows(rows)

	logs, truncated, err := repo.SampleSystemLogsForDebugExport(context.Background(), start, end, 1)
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, logs, 1)
	require.Equal(t, int64(9), logs[0].ID)
	require.Equal(t, "handler.gateway.messages", logs[0].Component)
	require.Equal(t, "req-1", logs[0].RequestID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositorySampleErrorLogsForDebugExportUsesBoundedQuery(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	end := start.Add(6 * time.Hour)

	rows := sqlmock.NewRows([]string{
		"id",
		"created_at",
		"error_phase",
		"error_type",
		"error_owner",
		"error_source",
		"severity",
		"status_code",
		"platform",
		"model",
		"client_request_id",
		"request_id",
		"error_message",
		"request_path",
		"inbound_endpoint",
		"upstream_endpoint",
		"requested_model",
		"upstream_model",
		"request_type",
	}).
		AddRow(int64(12), end.Add(-time.Minute), "upstream", "upstream_error", "provider", "upstream_http", "error", int64(500), "openai", "gpt-4o", "client-1", "req-1", "provider failed", "/v1/responses", "/v1/responses", "/v1/responses", "gpt-4o", "gpt-4o", int64(1)).
		AddRow(int64(11), end.Add(-2*time.Minute), "routing", "no_account", "platform", "gateway", "warn", int64(503), "openai", "gpt-4o-mini", "client-2", "req-2", "no account", "/v1/chat/completions", "/v1/chat/completions", "", "gpt-4o-mini", "", nil)

	mock.ExpectQuery(`(?s)FROM ops_error_logs e\s+WHERE e\.created_at >= \$1 AND e\.created_at < \$2\s+AND COALESCE\(e\.status_code, 0\) >= 400\s+ORDER BY e\.created_at DESC, e\.id DESC\s+LIMIT \$3`).
		WithArgs(start, end, 2).
		WillReturnRows(rows)

	logs, truncated, err := repo.SampleErrorLogsForDebugExport(context.Background(), start, end, 1)
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, logs, 1)
	require.Equal(t, int64(12), logs[0].ID)
	require.Equal(t, "upstream", logs[0].Phase)
	require.Equal(t, 500, logs[0].StatusCode)
	require.Equal(t, "req-1", logs[0].RequestID)
	require.NotNil(t, logs[0].RequestType)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOpsRepositorySampleLogsForDebugExportZeroLimitSkipsQueries(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &opsRepository{db: db}
	start := time.Date(2026, 5, 20, 8, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	systemLogs, systemTruncated, err := repo.SampleSystemLogsForDebugExport(context.Background(), start, end, 0)
	require.NoError(t, err)
	require.False(t, systemTruncated)
	require.Empty(t, systemLogs)

	errorLogs, errorTruncated, err := repo.SampleErrorLogsForDebugExport(context.Background(), start, end, 0)
	require.NoError(t, err)
	require.False(t, errorTruncated)
	require.Empty(t, errorLogs)
	require.NoError(t, mock.ExpectationsWereMet())
}
