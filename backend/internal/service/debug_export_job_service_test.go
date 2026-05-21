package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type debugExportJobRepoStub struct {
	mu              sync.Mutex
	created         []*DebugExportJob
	statusByID      map[int64]string
	cancelCalls     []int64
	cancelResult    bool
	getJob          *DebugExportJob
	getJobErr       error
	expiredJobs     []DebugExportJob
	markedExpired   []int64
	listRecentJobs  []DebugExportJob
	listRecentError error
	markSuccessErr  error
	createLimits    *DebugExportJobCreateLimits
	activeGlobal    int
	activeByCreator map[int64]int
	recentByCreator map[int64]int
	retainedBytes   int64
	livePaths       []string
	failedMessages  []string
}

func (s *debugExportJobRepoStub) CreateJob(ctx context.Context, job *DebugExportJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.ID == 0 {
		job.ID = int64(len(s.created) + 1)
	}
	job.CreatedAt = time.Now().UTC()
	job.UpdatedAt = job.CreatedAt
	clone := *job
	s.created = append(s.created, &clone)
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	s.statusByID[job.ID] = job.Status
	return nil
}

func (s *debugExportJobRepoStub) CreateJobWithLimits(ctx context.Context, job *DebugExportJob, limits DebugExportJobCreateLimits) error {
	s.mu.Lock()
	s.createLimits = &limits
	activeByCreator := 0
	if s.activeByCreator != nil {
		activeByCreator = s.activeByCreator[job.CreatedBy]
	}
	recentByCreator := 0
	if s.recentByCreator != nil {
		recentByCreator = s.recentByCreator[job.CreatedBy]
	}
	activeGlobal := s.activeGlobal
	retainedBytes := s.retainedBytes
	s.mu.Unlock()

	if activeByCreator >= limits.MaxActiveJobsPerCreator {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_TOO_MANY_ACTIVE_JOBS", "too many active debug export jobs for this admin")
	}
	if activeGlobal >= limits.MaxActiveJobs {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_QUEUE_FULL", "too many active debug export jobs")
	}
	if recentByCreator >= limits.MaxJobsPerCreatorWindow {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_CREATE_RATE_LIMITED", "too many debug export jobs created recently")
	}
	reservedBytes := retainedBytes + int64(activeGlobal+1)*limits.MaxArtifactBytes
	if reservedBytes > limits.MaxRetainedArtifactBytes {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED", "debug export artifact storage quota is full")
	}
	return s.CreateJob(ctx, job)
}

func (s *debugExportJobRepoStub) ListRecentJobs(ctx context.Context, limit int) ([]DebugExportJob, error) {
	return s.listRecentJobs, s.listRecentError
}

func (s *debugExportJobRepoStub) GetJob(ctx context.Context, jobID int64) (*DebugExportJob, error) {
	if s.getJobErr != nil {
		return nil, s.getJobErr
	}
	if s.getJob == nil {
		return nil, sql.ErrNoRows
	}
	clone := *s.getJob
	return &clone, nil
}

func (s *debugExportJobRepoStub) GetJobStatus(ctx context.Context, jobID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusByID == nil {
		return "", sql.ErrNoRows
	}
	status, ok := s.statusByID[jobID]
	if !ok {
		return "", sql.ErrNoRows
	}
	return status, nil
}

func (s *debugExportJobRepoStub) CountActiveJobs(ctx context.Context) (int, error) {
	return s.activeGlobal, nil
}

func (s *debugExportJobRepoStub) CountActiveJobsByCreator(ctx context.Context, createdBy int64) (int, error) {
	if s.activeByCreator == nil {
		return 0, nil
	}
	return s.activeByCreator[createdBy], nil
}

func (s *debugExportJobRepoStub) CountRecentJobsByCreator(ctx context.Context, createdBy int64, since time.Time) (int, error) {
	if s.recentByCreator == nil {
		return 0, nil
	}
	return s.recentByCreator[createdBy], nil
}

func (s *debugExportJobRepoStub) SumRetainedArtifactBytes(ctx context.Context, now time.Time) (int64, error) {
	return s.retainedBytes, nil
}

func (s *debugExportJobRepoStub) ListLiveArtifactPaths(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.livePaths, nil
}

func (s *debugExportJobRepoStub) ClaimNextPendingJob(ctx context.Context, staleRunningAfterSeconds int64) (*DebugExportJob, error) {
	return nil, nil
}

func (s *debugExportJobRepoStub) UpdateJobProgress(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64) error {
	return nil
}

func (s *debugExportJobRepoStub) CancelJob(ctx context.Context, jobID int64, canceledBy int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelCalls = append(s.cancelCalls, jobID)
	if s.statusByID == nil {
		s.statusByID = map[int64]string{}
	}
	s.statusByID[jobID] = DebugExportJobStatusCanceled
	return s.cancelResult, nil
}

func (s *debugExportJobRepoStub) MarkJobSucceeded(ctx context.Context, jobID int64, percent int, phase string, bytesWritten int64, fileName string, artifactPath string, fileSize int64, sha256 string, expiresAt time.Time) error {
	return s.markSuccessErr
}

func (s *debugExportJobRepoStub) MarkJobFailed(ctx context.Context, jobID int64, errorMsg string) error {
	s.failedMessages = append(s.failedMessages, errorMsg)
	return nil
}

func (s *debugExportJobRepoStub) ListExpiredSucceededJobs(ctx context.Context, now time.Time, limit int) ([]DebugExportJob, error) {
	return s.expiredJobs, nil
}

func (s *debugExportJobRepoStub) MarkJobExpired(ctx context.Context, jobID int64) error {
	s.markedExpired = append(s.markedExpired, jobID)
	return nil
}

func TestDebugExportJobServiceCreateJobNormalizesOptions(t *testing.T) {
	repo := &debugExportJobRepoStub{}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	job, err := svc.CreateJob(context.Background(), SystemDebugExportOptions{}, 9)
	require.NoError(t, err)
	require.Equal(t, int64(1), job.ID)
	require.Equal(t, DebugExportJobStatusPending, job.Status)
	require.Equal(t, SystemDebugExportDetailStandard, job.Options.DetailLevel)
	require.Equal(t, SystemDebugExportSensitiveMasked, job.Options.SensitiveHandling)
	require.Empty(t, job.Options.LogWindowPreset)
	require.NotNil(t, repo.createLimits)
	require.Equal(t, debugExportMaxActiveJobs, repo.createLimits.MaxActiveJobs)
	require.Equal(t, debugExportMaxActiveJobsPerCreator, repo.createLimits.MaxActiveJobsPerCreator)
}

func TestDebugExportJobServiceCreateJobRejectsInvalidCustomWindow(t *testing.T) {
	repo := &debugExportJobRepoStub{}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	_, err := svc.CreateJob(context.Background(), SystemDebugExportOptions{
		LogWindowPreset: SystemDebugExportLogWindowCustom,
		CustomLogStart:  "2026-05-20T10:00:00Z",
		CustomLogEnd:    "2026-05-20T09:00:00Z",
	}, 9)

	require.Error(t, err)
	require.Contains(t, err.Error(), "custom_log_start must be before custom_log_end")
	require.Empty(t, repo.created)
}

func TestDebugExportJobServiceCreateJobEnforcesActiveQuota(t *testing.T) {
	repo := &debugExportJobRepoStub{activeByCreator: map[int64]int{9: debugExportMaxActiveJobsPerCreator}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	_, err := svc.CreateJob(context.Background(), SystemDebugExportOptions{}, 9)

	require.Error(t, err)
	require.Contains(t, err.Error(), "DEBUG_EXPORT_TOO_MANY_ACTIVE_JOBS")
	require.Empty(t, repo.created)
}

func TestDebugExportJobServiceCreateJobEnforcesArtifactQuota(t *testing.T) {
	repo := &debugExportJobRepoStub{retainedBytes: debugExportMaxRetainedArtifactBytes}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	_, err := svc.CreateJob(context.Background(), SystemDebugExportOptions{}, 9)

	require.Error(t, err)
	require.Contains(t, err.Error(), "DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED")
	require.Empty(t, repo.created)
}

func TestDebugExportJobServiceCreateJobReservesActiveArtifactCapacity(t *testing.T) {
	repo := &debugExportJobRepoStub{
		activeGlobal:  1,
		retainedBytes: debugExportMaxRetainedArtifactBytes - debugExportMaxArtifactBytes,
	}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	_, err := svc.CreateJob(context.Background(), SystemDebugExportOptions{}, 9)

	require.Error(t, err)
	require.Contains(t, err.Error(), "DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED")
	require.Empty(t, repo.created)
}

func TestDebugExportJobServiceCancelJob(t *testing.T) {
	ok := true
	repo := &debugExportJobRepoStub{statusByID: map[int64]string{3: DebugExportJobStatusRunning}, cancelResult: ok}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	err := svc.CancelJob(context.Background(), 3, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{3}, repo.cancelCalls)
}

func TestDebugExportJobServiceOpenDownloadRejectsTraversal(t *testing.T) {
	expiresAt := time.Now().Add(time.Hour)
	fileName := "debug.json"
	badPath := "../debug.json"
	repo := &debugExportJobRepoStub{getJob: &DebugExportJob{ID: 1, Status: DebugExportJobStatusSucceeded, FileName: &fileName, ArtifactPath: &badPath, ExpiresAt: &expiresAt}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = t.TempDir()

	_, err := svc.OpenDownload(context.Background(), 1)
	require.Error(t, err)
}

func TestDebugExportJobServiceOpenDownloadStreamsFile(t *testing.T) {
	baseDir := t.TempDir()
	fileName := "debug.json"
	relPath := "debug.json"
	path := filepath.Join(baseDir, relPath)
	require.NoError(t, os.WriteFile(path, []byte(`{"ok":true}`), 0600))
	expiresAt := time.Now().Add(time.Hour)
	repo := &debugExportJobRepoStub{getJob: &DebugExportJob{ID: 2, Status: DebugExportJobStatusSucceeded, FileName: &fileName, ArtifactPath: &relPath, ExpiresAt: &expiresAt}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = baseDir

	download, err := svc.OpenDownload(context.Background(), 2)
	require.NoError(t, err)
	defer func() { _ = download.Reader.Close() }()
	require.Equal(t, fileName, download.FileName)
	require.Equal(t, int64(11), download.SizeBytes)
}

func TestDebugExportJobServiceOpenDownloadRejectsSymlink(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "target.json")
	require.NoError(t, os.WriteFile(target, []byte(`{"ok":true}`), 0600))
	relPath := "debug.json"
	linkPath := filepath.Join(baseDir, relPath)
	if err := os.Symlink(target, linkPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	fileName := "debug.json"
	expiresAt := time.Now().Add(time.Hour)
	repo := &debugExportJobRepoStub{getJob: &DebugExportJob{ID: 22, Status: DebugExportJobStatusSucceeded, FileName: &fileName, ArtifactPath: &relPath, ExpiresAt: &expiresAt}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = baseDir

	_, err := svc.OpenDownload(context.Background(), 22)
	require.Error(t, err)
}

func TestDebugExportJobServiceCleanupExpiredRemovesArtifacts(t *testing.T) {
	baseDir := t.TempDir()
	relPath := "expired.json"
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, relPath), []byte("{}"), 0600))
	repo := &debugExportJobRepoStub{expiredJobs: []DebugExportJob{{ID: 4, ArtifactPath: &relPath}}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = baseDir

	svc.cleanupExpired()

	_, err := os.Stat(filepath.Join(baseDir, relPath))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, []int64{4}, repo.markedExpired)
}

func TestDebugExportJobServiceCleanupRemovesStaleOrphanArtifacts(t *testing.T) {
	baseDir := t.TempDir()
	orphan := "sub2api-debug-export-99-20260520T120000Z.json"
	live := "sub2api-debug-export-100-20260520T120000Z.json"
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, orphan), []byte("{}"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, live), []byte("{}"), 0600))
	oldTime := time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(baseDir, orphan), oldTime, oldTime))
	require.NoError(t, os.Chtimes(filepath.Join(baseDir, live), oldTime, oldTime))
	repo := &debugExportJobRepoStub{livePaths: []string{live}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = baseDir
	svc.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	svc.cleanupExpired()

	_, err := os.Stat(filepath.Join(baseDir, orphan))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Stat(filepath.Join(baseDir, live))
	require.NoError(t, err)
}

func TestDebugExportJobServiceExecuteJobRemovesArtifactWhenSuccessMarkFails(t *testing.T) {
	baseDir := t.TempDir()
	repo := &debugExportJobRepoStub{
		statusByID:     map[int64]string{7: DebugExportJobStatusRunning},
		markSuccessErr: sql.ErrNoRows,
	}
	exporter := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{Version: "test", BuildType: "test"}, testSystemDebugOpsCounters{})
	exporter.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }
	svc := NewDebugExportJobService(repo, exporter, nil, nil)
	svc.baseDir = baseDir
	svc.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	svc.executeJob(context.Background(), &DebugExportJob{ID: 7, Status: DebugExportJobStatusRunning, Options: SystemDebugExportOptions{}})

	entries, err := os.ReadDir(baseDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestDebugExportJobServiceExecuteJobMarksFailureWhenSuccessMarkErrors(t *testing.T) {
	baseDir := t.TempDir()
	boom := errors.New("database unavailable")
	repo := &debugExportJobRepoStub{
		statusByID:     map[int64]string{8: DebugExportJobStatusRunning},
		markSuccessErr: boom,
	}
	exporter := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{Version: "test", BuildType: "test"}, testSystemDebugOpsCounters{})
	svc := NewDebugExportJobService(repo, exporter, nil, nil)
	svc.baseDir = baseDir

	svc.executeJob(context.Background(), &DebugExportJob{ID: 8, Status: DebugExportJobStatusRunning, Options: SystemDebugExportOptions{}})

	entries, err := os.ReadDir(baseDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestDebugExportJobServiceExecuteJobRechecksArtifactQuotaBeforeRename(t *testing.T) {
	baseDir := t.TempDir()
	repo := &debugExportJobRepoStub{
		statusByID:    map[int64]string{9: DebugExportJobStatusRunning},
		retainedBytes: debugExportMaxRetainedArtifactBytes,
	}
	exporter := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{Version: "test", BuildType: "test"}, testSystemDebugOpsCounters{})
	svc := NewDebugExportJobService(repo, exporter, nil, nil)
	svc.baseDir = baseDir

	svc.executeJob(context.Background(), &DebugExportJob{ID: 9, Status: DebugExportJobStatusRunning, Options: SystemDebugExportOptions{}})

	entries, err := os.ReadDir(baseDir)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.Len(t, repo.failedMessages, 1)
	require.Contains(t, repo.failedMessages[0], "DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED")
}

func TestDebugExportJobServiceExecuteJobMarksFailedWhenContextStops(t *testing.T) {
	baseDir := t.TempDir()
	repo := &debugExportJobRepoStub{statusByID: map[int64]string{11: DebugExportJobStatusRunning}}
	exporter := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{Version: "test", BuildType: "test"}, testSystemDebugOpsCounters{})
	svc := NewDebugExportJobService(repo, exporter, nil, nil)
	svc.baseDir = baseDir
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	svc.executeJob(ctx, &DebugExportJob{ID: 11, Status: DebugExportJobStatusRunning, Options: SystemDebugExportOptions{}})

	require.Len(t, repo.failedMessages, 1)
	require.Contains(t, repo.failedMessages[0], "worker stopped")
}

func TestDebugExportJobServiceCleanupSkipsOrphansWhenLivePathLimitReached(t *testing.T) {
	baseDir := t.TempDir()
	orphan := "sub2api-debug-export-101-20260520T120000Z.json"
	require.NoError(t, os.WriteFile(filepath.Join(baseDir, orphan), []byte("{}"), 0600))
	oldTime := time.Date(2026, 5, 20, 11, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(baseDir, orphan), oldTime, oldTime))
	livePaths := make([]string, debugExportLiveArtifactPathLimit)
	for i := range livePaths {
		livePaths[i] = "live.json"
	}
	repo := &debugExportJobRepoStub{livePaths: livePaths}
	svc := NewDebugExportJobService(repo, nil, nil, nil)
	svc.baseDir = baseDir
	svc.now = func() time.Time { return time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC) }

	svc.cleanupExpired()

	_, err := os.Stat(filepath.Join(baseDir, orphan))
	require.NoError(t, err)
}

func TestDebugExportJobServiceMarkFailedRedactsSensitiveText(t *testing.T) {
	repo := &debugExportJobRepoStub{statusByID: map[int64]string{10: DebugExportJobStatusRunning}}
	svc := NewDebugExportJobService(repo, nil, nil, nil)

	svc.markFailed(10, errors.New("failed with access_token=secret-token password=raw-password"))

	require.Len(t, repo.failedMessages, 1)
	require.NotContains(t, repo.failedMessages[0], "secret-token")
	require.NotContains(t, repo.failedMessages[0], "raw-password")
	require.Contains(t, repo.failedMessages[0], "***")
}
