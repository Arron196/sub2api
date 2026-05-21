package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func debugExportJobRows() []string {
	return []string{
		"id", "status", "options", "created_by", "progress_percent", "phase", "bytes_written",
		"file_name", "artifact_path", "file_size", "sha256", "error_message", "canceled_by", "canceled_at",
		"started_at", "finished_at", "expires_at", "last_heartbeat_at", "created_at", "updated_at",
	}
}

func TestDebugExportJobRepositoryCreateJob(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	job := &service.DebugExportJob{
		Status:          service.DebugExportJobStatusPending,
		Options:         service.SystemDebugExportOptions{DetailLevel: service.SystemDebugExportDetailSupport, SensitiveHandling: service.SystemDebugExportSensitiveMasked, LogWindowPreset: service.SystemDebugExportLogWindowLast1Day},
		CreatedBy:       7,
		ProgressPercent: 0,
		Phase:           "queued",
	}

	mock.ExpectQuery("INSERT INTO debug_export_jobs").
		WithArgs(job.Status, sqlmock.AnyArg(), job.CreatedBy, job.ProgressPercent, job.Phase, job.BytesWritten).
		WillReturnRows(sqlmock.NewRows(debugExportJobRows()).AddRow(
			int64(11), job.Status, `{"detail_level":"support","sensitive_handling":"masked","log_window_preset":"1d"}`, job.CreatedBy, 0, "queued", int64(0),
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, now, now,
		))

	err := repo.CreateJob(context.Background(), job)
	require.NoError(t, err)
	require.Equal(t, int64(11), job.ID)
	require.Equal(t, service.SystemDebugExportDetailSupport, job.Options.DetailLevel)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryCreateJobWithLimitsIsTransactional(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDebugExportJobRepositoryWithSQL(db)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	job := &service.DebugExportJob{
		Status:          service.DebugExportJobStatusPending,
		Options:         service.SystemDebugExportOptions{DetailLevel: service.SystemDebugExportDetailSupport, SensitiveHandling: service.SystemDebugExportSensitiveMasked, LogWindowPreset: service.SystemDebugExportLogWindowLast1Day},
		CreatedBy:       7,
		ProgressPercent: 0,
		Phase:           "queued",
	}
	limits := service.DebugExportJobCreateLimits{
		Now:                      now,
		RecentSince:              now.Add(-time.Hour),
		MaxActiveJobs:            5,
		MaxActiveJobsPerCreator:  2,
		MaxJobsPerCreatorWindow:  10,
		MaxRetainedArtifactBytes: 512 * 1024 * 1024,
		MaxArtifactBytes:         50 * 1024 * 1024,
	}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(debugExportJobCreateAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*created_by = \$1 AND status IN`).
		WithArgs(job.CreatedBy, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*FROM debug_export_jobs.*status IN`).
		WithArgs(service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*created_by = \$1 AND created_at >= \$2`).
		WithArgs(job.CreatedBy, limits.RecentSince).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT COALESCE\(SUM\(file_size\), 0\).*FROM debug_export_jobs`).
		WithArgs(service.DebugExportJobStatusSucceeded, limits.Now).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(int64(0)))
	mock.ExpectQuery("INSERT INTO debug_export_jobs").
		WithArgs(job.Status, sqlmock.AnyArg(), job.CreatedBy, job.ProgressPercent, job.Phase, job.BytesWritten).
		WillReturnRows(sqlmock.NewRows(debugExportJobRows()).AddRow(
			int64(12), job.Status, `{"detail_level":"support","sensitive_handling":"masked","log_window_preset":"1d"}`, job.CreatedBy, 0, "queued", int64(0),
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, now, now,
		))
	mock.ExpectCommit()

	err := repo.CreateJobWithLimits(context.Background(), job, limits)
	require.NoError(t, err)
	require.Equal(t, int64(12), job.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryCreateJobWithLimitsRollsBackOnQuota(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := newDebugExportJobRepositoryWithSQL(db)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	job := &service.DebugExportJob{Status: service.DebugExportJobStatusPending, CreatedBy: 7, Phase: "queued"}
	limits := service.DebugExportJobCreateLimits{
		Now:                     now,
		RecentSince:             now.Add(-time.Hour),
		MaxActiveJobs:           5,
		MaxActiveJobsPerCreator: 2,
		MaxJobsPerCreatorWindow: 10,
		MaxArtifactBytes:        50 * 1024 * 1024,
	}

	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs(debugExportJobCreateAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COUNT\(\*\).*created_by = \$1 AND status IN`).
		WithArgs(job.CreatedBy, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()

	err := repo.CreateJobWithLimits(context.Background(), job, limits)
	require.Error(t, err)
	require.Contains(t, err.Error(), "DEBUG_EXPORT_TOO_MANY_ACTIVE_JOBS")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryClaimNextPendingJobNone(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}

	mock.ExpectQuery("WITH next AS").
		WithArgs(service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning, int64(1800), service.DebugExportJobStatusRunning).
		WillReturnError(sql.ErrNoRows)

	job, err := repo.ClaimNextPendingJob(context.Background(), 0)
	require.NoError(t, err)
	require.Nil(t, job)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryMarkJobSucceeded(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}
	expiresAt := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec("UPDATE debug_export_jobs").
		WithArgs(service.DebugExportJobStatusSucceeded, 100, "ready", int64(123), "debug.json", "debug.json", int64(123), "abc123", expiresAt, int64(5), service.DebugExportJobStatusRunning).
		WillReturnResult(sqlmock.NewResult(0, 1))

	err := repo.MarkJobSucceeded(context.Background(), 5, 100, "ready", 123, "debug.json", "debug.json", 123, "abc123", expiresAt)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryMarkJobSucceededReturnsNoRows(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}
	expiresAt := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec("UPDATE debug_export_jobs").
		WithArgs(service.DebugExportJobStatusSucceeded, 100, "ready", int64(123), "debug.json", "debug.json", int64(123), "abc123", expiresAt, int64(5), service.DebugExportJobStatusRunning).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := repo.MarkJobSucceeded(context.Background(), 5, 100, "ready", 123, "debug.json", "debug.json", 123, "abc123", expiresAt)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryQuotaQueries(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*FROM debug_export_jobs.*status IN").
		WithArgs(service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	active, err := repo.CountActiveJobs(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, active)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*created_by = \\$1 AND status IN").
		WithArgs(int64(9), service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	byCreator, err := repo.CountActiveJobsByCreator(context.Background(), 9)
	require.NoError(t, err)
	require.Equal(t, 2, byCreator)

	mock.ExpectQuery("SELECT COUNT\\(\\*\\).*created_by = \\$1 AND created_at >= \\$2").
		WithArgs(int64(9), now.Add(-time.Hour)).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(7))
	recent, err := repo.CountRecentJobsByCreator(context.Background(), 9, now.Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 7, recent)

	mock.ExpectQuery("SELECT COALESCE\\(SUM\\(file_size\\), 0\\).*FROM debug_export_jobs").
		WithArgs(service.DebugExportJobStatusSucceeded, now).
		WillReturnRows(sqlmock.NewRows([]string{"total"}).AddRow(int64(1234)))
	total, err := repo.SumRetainedArtifactBytes(context.Background(), now)
	require.NoError(t, err)
	require.Equal(t, int64(1234), total)

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDebugExportJobRepositoryListLiveArtifactPaths(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &debugExportJobRepository{sql: db}
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	mock.ExpectQuery("SELECT artifact_path.*FROM debug_export_jobs").
		WithArgs(service.DebugExportJobStatusSucceeded, now, 2).
		WillReturnRows(sqlmock.NewRows([]string{"artifact_path"}).AddRow("a.json").AddRow("b.json"))

	paths, err := repo.ListLiveArtifactPaths(context.Background(), now, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"a.json", "b.json"}, paths)
	require.NoError(t, mock.ExpectationsWereMet())
}
