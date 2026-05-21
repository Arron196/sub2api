package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const debugExportJobCreateAdvisoryLockID int64 = 0x5db2a91e

type debugExportJobRepository struct {
	sql sqlExecutor
	db  *sql.DB
}

func NewDebugExportJobRepository(_ *dbent.Client, sqlDB *sql.DB) service.DebugExportJobRepository {
	return newDebugExportJobRepositoryWithSQL(sqlDB)
}

func newDebugExportJobRepositoryWithSQL(sqlq sqlExecutor) *debugExportJobRepository {
	repo := &debugExportJobRepository{sql: sqlq}
	if db, ok := sqlq.(*sql.DB); ok {
		repo.db = db
	}
	return repo
}

func (r *debugExportJobRepository) CreateJob(ctx context.Context, job *service.DebugExportJob) error {
	if job == nil {
		return nil
	}
	optionsJSON, err := json.Marshal(job.Options)
	if err != nil {
		return err
	}
	query := `
		INSERT INTO debug_export_jobs (status, options, created_by, progress_percent, phase, bytes_written)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, status, options, created_by, progress_percent, phase, bytes_written,
			file_name, artifact_path, file_size, sha256, error_message, canceled_by, canceled_at,
			started_at, finished_at, expires_at, last_heartbeat_at, created_at, updated_at
	`
	return r.scanJob(ctx, query, []any{job.Status, optionsJSON, job.CreatedBy, job.ProgressPercent, job.Phase, job.BytesWritten}, job)
}

func (r *debugExportJobRepository) CreateJobWithLimits(ctx context.Context, job *service.DebugExportJob, limits service.DebugExportJobCreateLimits) error {
	if job == nil {
		return nil
	}
	if r.db == nil {
		return fmt.Errorf("debug export job repository does not support transactional create")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, debugExportJobCreateAdvisoryLockID); err != nil {
		return err
	}
	if err := r.enforceCreateLimitsLocked(ctx, tx, job.CreatedBy, limits); err != nil {
		return err
	}
	txRepo := &debugExportJobRepository{sql: tx}
	if err := txRepo.CreateJob(ctx, job); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *debugExportJobRepository) enforceCreateLimitsLocked(ctx context.Context, q sqlQueryer, createdBy int64, limits service.DebugExportJobCreateLimits) error {
	activeByCreator, err := scanDebugExportJobCount(ctx, q, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE created_by = $1 AND status IN ($2, $3)
	`, createdBy, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning)
	if err != nil {
		return fmt.Errorf("count active debug export jobs by creator: %w", err)
	}
	if activeByCreator >= limits.MaxActiveJobsPerCreator {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_TOO_MANY_ACTIVE_JOBS", "too many active debug export jobs for this admin")
	}

	activeGlobal, err := scanDebugExportJobCount(ctx, q, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE status IN ($1, $2)
	`, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning)
	if err != nil {
		return fmt.Errorf("count active debug export jobs: %w", err)
	}
	if activeGlobal >= limits.MaxActiveJobs {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_QUEUE_FULL", "too many active debug export jobs")
	}

	recentByCreator, err := scanDebugExportJobCount(ctx, q, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE created_by = $1 AND created_at >= $2
	`, createdBy, limits.RecentSince)
	if err != nil {
		return fmt.Errorf("count recent debug export jobs: %w", err)
	}
	if recentByCreator >= limits.MaxJobsPerCreatorWindow {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_CREATE_RATE_LIMITED", "too many debug export jobs created recently")
	}

	retainedBytes, err := scanDebugExportJobBytes(ctx, q, `
		SELECT COALESCE(SUM(file_size), 0)
		FROM debug_export_jobs
		WHERE status = $1 AND expires_at IS NOT NULL AND expires_at > $2
	`, service.DebugExportJobStatusSucceeded, limits.Now)
	if err != nil {
		return fmt.Errorf("sum debug export artifact bytes: %w", err)
	}
	reservedBytes := retainedBytes + int64(activeGlobal+1)*limits.MaxArtifactBytes
	if reservedBytes > limits.MaxRetainedArtifactBytes {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED", "debug export artifact storage quota is full")
	}
	return nil
}

func (r *debugExportJobRepository) ListRecentJobs(ctx context.Context, limit int) ([]service.DebugExportJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, status, options, created_by, progress_percent, phase, bytes_written,
			file_name, artifact_path, file_size, sha256, error_message, canceled_by, canceled_at,
			started_at, finished_at, expires_at, last_heartbeat_at, created_at, updated_at
		FROM debug_export_jobs
		ORDER BY created_at DESC, id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]service.DebugExportJob, 0)
	for rows.Next() {
		job, err := scanDebugExportJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *debugExportJobRepository) GetJob(ctx context.Context, jobID int64) (*service.DebugExportJob, error) {
	var job service.DebugExportJob
	if err := r.scanJob(ctx, `
		SELECT id, status, options, created_by, progress_percent, phase, bytes_written,
			file_name, artifact_path, file_size, sha256, error_message, canceled_by, canceled_at,
			started_at, finished_at, expires_at, last_heartbeat_at, created_at, updated_at
		FROM debug_export_jobs
		WHERE id = $1
	`, []any{jobID}, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *debugExportJobRepository) GetJobStatus(ctx context.Context, jobID int64) (string, error) {
	var status string
	if err := scanSingleRow(ctx, r.sql, `SELECT status FROM debug_export_jobs WHERE id = $1`, []any{jobID}, &status); err != nil {
		return "", err
	}
	return status, nil
}

func (r *debugExportJobRepository) CountActiveJobs(ctx context.Context) (int, error) {
	return r.countJobs(ctx, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE status IN ($1, $2)
	`, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning)
}

func (r *debugExportJobRepository) CountActiveJobsByCreator(ctx context.Context, createdBy int64) (int, error) {
	return r.countJobs(ctx, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE created_by = $1 AND status IN ($2, $3)
	`, createdBy, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning)
}

func (r *debugExportJobRepository) CountRecentJobsByCreator(ctx context.Context, createdBy int64, since time.Time) (int, error) {
	return r.countJobs(ctx, `
		SELECT COUNT(*)
		FROM debug_export_jobs
		WHERE created_by = $1 AND created_at >= $2
	`, createdBy, since)
}

func (r *debugExportJobRepository) SumRetainedArtifactBytes(ctx context.Context, now time.Time) (int64, error) {
	var total sql.NullInt64
	if err := scanSingleRow(ctx, r.sql, `
		SELECT COALESCE(SUM(file_size), 0)
		FROM debug_export_jobs
		WHERE status = $1 AND expires_at IS NOT NULL AND expires_at > $2
	`, []any{service.DebugExportJobStatusSucceeded, now}, &total); err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (r *debugExportJobRepository) ListLiveArtifactPaths(ctx context.Context, now time.Time, limit int) ([]string, error) {
	if limit <= 0 || limit > 10000 {
		limit = 10000
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT artifact_path
		FROM debug_export_jobs
		WHERE artifact_path IS NOT NULL
			AND status = $1
			AND expires_at IS NOT NULL
			AND expires_at > $2
		ORDER BY id DESC
		LIMIT $3
	`, service.DebugExportJobStatusSucceeded, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	paths := make([]string, 0)
	for rows.Next() {
		var rel string
		if err := rows.Scan(&rel); err != nil {
			return nil, err
		}
		paths = append(paths, rel)
	}
	return paths, rows.Err()
}

func (r *debugExportJobRepository) countJobs(ctx context.Context, query string, args ...any) (int, error) {
	return scanDebugExportJobCount(ctx, r.sql, query, args...)
}

func scanDebugExportJobCount(ctx context.Context, q sqlQueryer, query string, args ...any) (int, error) {
	var count int
	if err := scanSingleRow(ctx, q, query, args, &count); err != nil {
		return 0, err
	}
	return count, nil
}

func scanDebugExportJobBytes(ctx context.Context, q sqlQueryer, query string, args ...any) (int64, error) {
	var total sql.NullInt64
	if err := scanSingleRow(ctx, q, query, args, &total); err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

func (r *debugExportJobRepository) ClaimNextPendingJob(ctx context.Context, staleRunningAfterSeconds int64) (*service.DebugExportJob, error) {
	if staleRunningAfterSeconds <= 0 {
		staleRunningAfterSeconds = 1800
	}
	query := `
		WITH next AS (
			SELECT id
			FROM debug_export_jobs
			WHERE status = $1
				OR (
					status = $2
					AND last_heartbeat_at IS NOT NULL
					AND last_heartbeat_at < NOW() - ($3 * interval '1 second')
				)
			ORDER BY created_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE debug_export_jobs AS jobs
		SET status = $4,
			started_at = COALESCE(jobs.started_at, NOW()),
			finished_at = NULL,
			error_message = NULL,
			last_heartbeat_at = NOW(),
			phase = 'starting',
			progress_percent = GREATEST(jobs.progress_percent, 5),
			updated_at = NOW()
		FROM next
		WHERE jobs.id = next.id
		RETURNING jobs.id, jobs.status, jobs.options, jobs.created_by, jobs.progress_percent, jobs.phase, jobs.bytes_written,
			jobs.file_name, jobs.artifact_path, jobs.file_size, jobs.sha256, jobs.error_message, jobs.canceled_by, jobs.canceled_at,
			jobs.started_at, jobs.finished_at, jobs.expires_at, jobs.last_heartbeat_at, jobs.created_at, jobs.updated_at
	`
	var job service.DebugExportJob
	if err := r.scanJob(ctx, query, []any{service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning, staleRunningAfterSeconds, service.DebugExportJobStatusRunning}, &job); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &job, nil
}

func (r *debugExportJobRepository) UpdateJobProgress(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE debug_export_jobs
		SET progress_percent = $1, phase = $2, bytes_written = $3, last_heartbeat_at = NOW(), updated_at = NOW()
		WHERE id = $4 AND status = $5
	`, percent, phase, bytesWritten, jobID, service.DebugExportJobStatusRunning)
	return err
}

func (r *debugExportJobRepository) CancelJob(ctx context.Context, jobID int64, canceledBy int64) (bool, error) {
	var id int64
	err := scanSingleRow(ctx, r.sql, `
		UPDATE debug_export_jobs
		SET status = $1, canceled_by = $2, canceled_at = NOW(), finished_at = NOW(), phase = 'canceled', updated_at = NOW()
		WHERE id = $3 AND status IN ($4, $5)
		RETURNING id
	`, []any{service.DebugExportJobStatusCanceled, canceledBy, jobID, service.DebugExportJobStatusPending, service.DebugExportJobStatusRunning}, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (r *debugExportJobRepository) MarkJobSucceeded(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64, fileName string, artifactPath string, fileSize int64, sha256 string, expiresAt time.Time) error {
	result, err := r.sql.ExecContext(ctx, `
		UPDATE debug_export_jobs
		SET status = $1, progress_percent = $2, phase = $3, bytes_written = $4,
			file_name = $5, artifact_path = $6, file_size = $7, sha256 = $8,
			expires_at = $9, finished_at = NOW(), last_heartbeat_at = NOW(), updated_at = NOW()
		WHERE id = $10 AND status = $11
	`, service.DebugExportJobStatusSucceeded, percent, phase, bytesWritten, fileName, artifactPath, fileSize, sha256, expiresAt, jobID, service.DebugExportJobStatusRunning)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *debugExportJobRepository) MarkJobFailed(ctx context.Context, jobID int64, errorMsg string) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE debug_export_jobs
		SET status = $1, phase = 'failed', error_message = $2, finished_at = NOW(), updated_at = NOW()
		WHERE id = $3 AND status = $4
	`, service.DebugExportJobStatusFailed, errorMsg, jobID, service.DebugExportJobStatusRunning)
	return err
}

func (r *debugExportJobRepository) ListExpiredSucceededJobs(ctx context.Context, now time.Time, limit int) ([]service.DebugExportJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.sql.QueryContext(ctx, `
		SELECT id, status, options, created_by, progress_percent, phase, bytes_written,
			file_name, artifact_path, file_size, sha256, error_message, canceled_by, canceled_at,
			started_at, finished_at, expires_at, last_heartbeat_at, created_at, updated_at
		FROM debug_export_jobs
		WHERE status = $1 AND expires_at IS NOT NULL AND expires_at <= $2
		ORDER BY expires_at ASC
		LIMIT $3
	`, service.DebugExportJobStatusSucceeded, now, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	jobs := make([]service.DebugExportJob, 0)
	for rows.Next() {
		job, err := scanDebugExportJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (r *debugExportJobRepository) MarkJobExpired(ctx context.Context, jobID int64) error {
	_, err := r.sql.ExecContext(ctx, `
		UPDATE debug_export_jobs
		SET status = $1, phase = 'expired', updated_at = NOW()
		WHERE id = $2 AND status = $3
	`, service.DebugExportJobStatusExpired, jobID, service.DebugExportJobStatusSucceeded)
	return err
}

func (r *debugExportJobRepository) scanJob(ctx context.Context, query string, args []any, job *service.DebugExportJob) error {
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	scanned, err := scanDebugExportJob(rows)
	if err != nil {
		return err
	}
	*job = scanned
	return rows.Err()
}

type debugExportJobScanner interface {
	Scan(dest ...any) error
}

func scanDebugExportJob(scanner debugExportJobScanner) (service.DebugExportJob, error) {
	var job service.DebugExportJob
	var optionsJSON []byte
	var fileName, artifactPath, sha256Value, errMsg sql.NullString
	var fileSize, canceledBy sql.NullInt64
	var canceledAt, startedAt, finishedAt, expiresAt, heartbeatAt sql.NullTime
	if err := scanner.Scan(
		&job.ID, &job.Status, &optionsJSON, &job.CreatedBy, &job.ProgressPercent, &job.Phase, &job.BytesWritten,
		&fileName, &artifactPath, &fileSize, &sha256Value, &errMsg, &canceledBy, &canceledAt,
		&startedAt, &finishedAt, &expiresAt, &heartbeatAt, &job.CreatedAt, &job.UpdatedAt,
	); err != nil {
		return job, err
	}
	if err := json.Unmarshal(optionsJSON, &job.Options); err != nil {
		return job, fmt.Errorf("parse debug export options: %w", err)
	}
	if fileName.Valid {
		job.FileName = &fileName.String
	}
	if artifactPath.Valid {
		job.ArtifactPath = &artifactPath.String
	}
	if fileSize.Valid {
		v := fileSize.Int64
		job.FileSize = &v
	}
	if sha256Value.Valid {
		job.SHA256 = &sha256Value.String
	}
	if errMsg.Valid {
		job.ErrorMsg = &errMsg.String
	}
	if canceledBy.Valid {
		v := canceledBy.Int64
		job.CanceledBy = &v
	}
	if canceledAt.Valid {
		job.CanceledAt = &canceledAt.Time
	}
	if startedAt.Valid {
		job.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		job.FinishedAt = &finishedAt.Time
	}
	if expiresAt.Valid {
		job.ExpiresAt = &expiresAt.Time
	}
	if heartbeatAt.Valid {
		job.LastHeartbeatAt = &heartbeatAt.Time
	}
	return job, nil
}
