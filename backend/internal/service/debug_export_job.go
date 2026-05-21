package service

import (
	"context"
	"io"
	"time"
)

const (
	DebugExportJobStatusPending   = "pending"
	DebugExportJobStatusRunning   = "running"
	DebugExportJobStatusSucceeded = "succeeded"
	DebugExportJobStatusFailed    = "failed"
	DebugExportJobStatusCanceled  = "canceled"
	DebugExportJobStatusExpired   = "expired"
)

type DebugExportJob struct {
	ID              int64                    `json:"id"`
	Status          string                   `json:"status"`
	Options         SystemDebugExportOptions `json:"options"`
	CreatedBy       int64                    `json:"created_by"`
	ProgressPercent int                      `json:"progress_percent"`
	Phase           string                   `json:"phase"`
	BytesWritten    int64                    `json:"bytes_written"`
	FileName        *string                  `json:"file_name,omitempty"`
	ArtifactPath    *string                  `json:"-"`
	FileSize        *int64                   `json:"file_size,omitempty"`
	SHA256          *string                  `json:"sha256,omitempty"`
	ErrorMsg        *string                  `json:"error_message,omitempty"`
	CanceledBy      *int64                   `json:"canceled_by,omitempty"`
	CanceledAt      *time.Time               `json:"canceled_at,omitempty"`
	StartedAt       *time.Time               `json:"started_at,omitempty"`
	FinishedAt      *time.Time               `json:"finished_at,omitempty"`
	ExpiresAt       *time.Time               `json:"expires_at,omitempty"`
	LastHeartbeatAt *time.Time               `json:"last_heartbeat_at,omitempty"`
	CreatedAt       time.Time                `json:"created_at"`
	UpdatedAt       time.Time                `json:"updated_at"`
}

type DebugExportJobRepository interface {
	CreateJob(ctx context.Context, job *DebugExportJob) error
	CreateJobWithLimits(ctx context.Context, job *DebugExportJob, limits DebugExportJobCreateLimits) error
	ListRecentJobs(ctx context.Context, limit int) ([]DebugExportJob, error)
	GetJob(ctx context.Context, jobID int64) (*DebugExportJob, error)
	GetJobStatus(ctx context.Context, jobID int64) (string, error)
	CountActiveJobs(ctx context.Context) (int, error)
	CountActiveJobsByCreator(ctx context.Context, createdBy int64) (int, error)
	CountRecentJobsByCreator(ctx context.Context, createdBy int64, since time.Time) (int, error)
	SumRetainedArtifactBytes(ctx context.Context, now time.Time) (int64, error)
	ListLiveArtifactPaths(ctx context.Context, now time.Time, limit int) ([]string, error)
	ClaimNextPendingJob(ctx context.Context, staleRunningAfterSeconds int64) (*DebugExportJob, error)
	UpdateJobProgress(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64) error
	CancelJob(ctx context.Context, jobID int64, canceledBy int64) (bool, error)
	MarkJobSucceeded(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64, fileName string, artifactPath string, fileSize int64, sha256 string, expiresAt time.Time) error
	MarkJobFailed(ctx context.Context, jobID int64, errorMsg string) error
	ListExpiredSucceededJobs(ctx context.Context, now time.Time, limit int) ([]DebugExportJob, error)
	MarkJobExpired(ctx context.Context, jobID int64) error
}

type DebugExportJobCreateLimits struct {
	Now                      time.Time
	RecentSince              time.Time
	MaxActiveJobs            int
	MaxActiveJobsPerCreator  int
	MaxJobsPerCreatorWindow  int
	MaxRetainedArtifactBytes int64
	MaxArtifactBytes         int64
}

type DebugExportDownload struct {
	Job         DebugExportJob
	FileName    string
	ContentType string
	SizeBytes   int64
	Reader      io.ReadCloser
}
