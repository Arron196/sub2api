package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/shirou/gopsutil/v4/disk"
)

const (
	debugExportJobWorkerName      = "debug_export_job_worker"
	debugExportJobCleanupName     = "debug_export_job_cleanup"
	debugExportArtifactRetain     = 24 * time.Hour
	debugExportJobWorkerInterval  = 10 * time.Second
	debugExportJobCleanupInterval = time.Hour
	debugExportJobTimeout         = 30 * time.Minute

	debugExportMaxActiveJobs            = 5
	debugExportMaxActiveJobsPerCreator  = 2
	debugExportMaxJobsPerCreatorWindow  = 10
	debugExportCreateWindow             = time.Hour
	debugExportMaxArtifactBytes         = 50 * 1024 * 1024
	debugExportMinFreeDiskBytes         = 100 * 1024 * 1024
	debugExportMaxRetainedArtifactBytes = 512 * 1024 * 1024
	debugExportTempArtifactGrace        = time.Hour
	debugExportOrphanArtifactGrace      = 10 * time.Minute
	debugExportLiveArtifactPathLimit    = 10000
	debugExportArtifactPrefix           = "sub2api-debug-export-"
	debugExportArtifactSuffix           = ".json"
)

var errDebugExportArtifactTooLarge = errors.New("debug export artifact exceeds configured size limit")

type DebugExportJobService struct {
	repo      DebugExportJobRepository
	exporter  *SystemDebugExportService
	timing    *TimingWheelService
	cfg       *config.Config
	baseDir   string
	now       func() time.Time
	running   int32
	startOnce sync.Once
	stopOnce  sync.Once

	workerCtx    context.Context
	workerCancel context.CancelFunc
}

func NewDebugExportJobService(repo DebugExportJobRepository, exporter *SystemDebugExportService, timing *TimingWheelService, cfg *config.Config) *DebugExportJobService {
	ctx, cancel := context.WithCancel(context.Background())
	return &DebugExportJobService{
		repo:         repo,
		exporter:     exporter,
		timing:       timing,
		cfg:          cfg,
		baseDir:      filepath.Join(debugExportDataDir(), "debug-exports"),
		now:          time.Now,
		workerCtx:    ctx,
		workerCancel: cancel,
	}
}

func debugExportDataDir() string {
	if dir := os.Getenv("DATA_DIR"); strings.TrimSpace(dir) != "" {
		return dir
	}
	dockerDataDir := "/app/data"
	if info, err := os.Stat(dockerDataDir); err == nil && info.IsDir() {
		testFile := filepath.Join(dockerDataDir, ".write_test")
		if f, err := os.Create(testFile); err == nil {
			_ = f.Close()
			_ = os.Remove(testFile)
			return dockerDataDir
		}
	}
	return "."
}

func ProvideDebugExportJobService(repo DebugExportJobRepository, exporter *SystemDebugExportService, timing *TimingWheelService, cfg *config.Config) *DebugExportJobService {
	svc := NewDebugExportJobService(repo, exporter, timing, cfg)
	svc.Start()
	return svc
}

func (s *DebugExportJobService) Start() {
	if s == nil || s.repo == nil || s.exporter == nil || s.timing == nil {
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] not started (missing deps)")
		return
	}
	s.startOnce.Do(func() {
		s.timing.ScheduleRecurring(debugExportJobWorkerName, debugExportJobWorkerInterval, s.runOnce)
		s.timing.ScheduleRecurring(debugExportJobCleanupName, debugExportJobCleanupInterval, s.cleanupExpired)
		go s.runOnce()
		go s.cleanupExpired()
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] started base_dir=%s", s.baseDir)
	})
}

func (s *DebugExportJobService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.workerCancel != nil {
			s.workerCancel()
		}
		if s.timing != nil {
			s.timing.Cancel(debugExportJobWorkerName)
			s.timing.Cancel(debugExportJobCleanupName)
		}
		s.removeTempFiles()
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] stopped")
	})
}

func (s *DebugExportJobService) CreateJob(ctx context.Context, options SystemDebugExportOptions, createdBy int64) (*DebugExportJob, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("debug export job service not ready")
	}
	if createdBy <= 0 {
		return nil, infraerrors.BadRequest("DEBUG_EXPORT_INVALID_CREATOR", "invalid creator")
	}
	normalized, err := NormalizeSystemDebugExportOptions(options)
	if err != nil {
		return nil, infraerrors.BadRequest("DEBUG_EXPORT_INVALID_OPTIONS", err.Error())
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	if _, err := resolveSystemDebugLogAttributionWindow(normalized, now); err != nil {
		return nil, infraerrors.BadRequest("DEBUG_EXPORT_INVALID_OPTIONS", err.Error())
	}
	job := &DebugExportJob{Status: DebugExportJobStatusPending, Options: normalized, CreatedBy: createdBy, Phase: "queued"}
	limits := DebugExportJobCreateLimits{
		Now:                      now,
		RecentSince:              now.Add(-debugExportCreateWindow),
		MaxActiveJobs:            debugExportMaxActiveJobs,
		MaxActiveJobsPerCreator:  debugExportMaxActiveJobsPerCreator,
		MaxJobsPerCreatorWindow:  debugExportMaxJobsPerCreatorWindow,
		MaxRetainedArtifactBytes: debugExportMaxRetainedArtifactBytes,
		MaxArtifactBytes:         debugExportMaxArtifactBytes,
	}
	if err := s.repo.CreateJobWithLimits(ctx, job, limits); err != nil {
		return nil, fmt.Errorf("create debug export job: %w", err)
	}
	go s.runOnce()
	return job, nil
}

func (s *DebugExportJobService) ListRecentJobs(ctx context.Context, limit int) ([]DebugExportJob, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("debug export job service not ready")
	}
	return s.repo.ListRecentJobs(ctx, limit)
}

func (s *DebugExportJobService) ensureRetainedArtifactCapacity(ctx context.Context, artifactSize int64) error {
	retainedBytes, err := s.repo.SumRetainedArtifactBytes(ctx, s.now())
	if err != nil {
		return fmt.Errorf("sum debug export artifact bytes: %w", err)
	}
	if retainedBytes+artifactSize > debugExportMaxRetainedArtifactBytes {
		return infraerrors.TooManyRequests("DEBUG_EXPORT_ARTIFACT_QUOTA_EXCEEDED", "debug export artifact storage quota is full")
	}
	return nil
}

func (s *DebugExportJobService) GetJob(ctx context.Context, id int64) (*DebugExportJob, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("debug export job service not ready")
	}
	job, err := s.repo.GetJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, infraerrors.NotFound("DEBUG_EXPORT_JOB_NOT_FOUND", "debug export job not found")
	}
	return job, err
}

func (s *DebugExportJobService) CancelJob(ctx context.Context, id int64, canceledBy int64) error {
	if canceledBy <= 0 {
		return infraerrors.BadRequest("DEBUG_EXPORT_INVALID_CANCELLER", "invalid canceller")
	}
	status, err := s.repo.GetJobStatus(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return infraerrors.NotFound("DEBUG_EXPORT_JOB_NOT_FOUND", "debug export job not found")
	}
	if err != nil {
		return err
	}
	if status == DebugExportJobStatusCanceled {
		return nil
	}
	if status != DebugExportJobStatusPending && status != DebugExportJobStatusRunning {
		return infraerrors.New(http.StatusConflict, "DEBUG_EXPORT_CANCEL_CONFLICT", "debug export job cannot be canceled in current status")
	}
	ok, err := s.repo.CancelJob(ctx, id, canceledBy)
	if err != nil {
		return err
	}
	if !ok {
		return infraerrors.New(http.StatusConflict, "DEBUG_EXPORT_CANCEL_CONFLICT", "debug export job cannot be canceled in current status")
	}
	return nil
}

func (s *DebugExportJobService) OpenDownload(ctx context.Context, id int64) (*DebugExportDownload, error) {
	job, err := s.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	if job.Status != DebugExportJobStatusSucceeded {
		return nil, infraerrors.New(http.StatusConflict, "DEBUG_EXPORT_NOT_READY", "debug export artifact is not ready")
	}
	if job.ExpiresAt == nil || !job.ExpiresAt.After(s.now()) {
		return nil, infraerrors.New(http.StatusGone, "DEBUG_EXPORT_EXPIRED", "debug export artifact has expired")
	}
	if job.ArtifactPath == nil || job.FileName == nil {
		return nil, infraerrors.NotFound("DEBUG_EXPORT_ARTIFACT_NOT_FOUND", "debug export artifact not found")
	}
	path, err := s.safeArtifactPath(*job.ArtifactPath)
	if err != nil {
		return nil, err
	}
	if targetInfo, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, infraerrors.NotFound("DEBUG_EXPORT_ARTIFACT_NOT_FOUND", "debug export artifact not found")
		}
		return nil, err
	} else if !targetInfo.Mode().IsRegular() || targetInfo.Mode()&os.ModeSymlink != 0 {
		return nil, infraerrors.NotFound("DEBUG_EXPORT_ARTIFACT_NOT_FOUND", "debug export artifact not found")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, infraerrors.NotFound("DEBUG_EXPORT_ARTIFACT_NOT_FOUND", "debug export artifact not found")
	}
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, infraerrors.NotFound("DEBUG_EXPORT_ARTIFACT_NOT_FOUND", "debug export artifact not found")
	}
	if info.Size() > debugExportMaxArtifactBytes {
		_ = file.Close()
		return nil, infraerrors.ServiceUnavailable("DEBUG_EXPORT_ARTIFACT_TOO_LARGE", "debug export artifact exceeds the configured size limit")
	}
	return &DebugExportDownload{Job: *job, FileName: *job.FileName, ContentType: "application/json", SizeBytes: info.Size(), Reader: file}, nil
}

func (s *DebugExportJobService) runOnce() {
	if s == nil || !atomic.CompareAndSwapInt32(&s.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&s.running, 0)
	parent := context.Background()
	if s.workerCtx != nil {
		parent = s.workerCtx
	}
	ctx, cancel := context.WithTimeout(parent, debugExportJobTimeout)
	defer cancel()
	job, err := s.repo.ClaimNextPendingJob(ctx, int64(debugExportJobTimeout.Seconds()))
	if err != nil || job == nil {
		if err != nil {
			logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] claim failed: %v", err)
		}
		return
	}
	s.executeJob(ctx, job)
}

func (s *DebugExportJobService) executeJob(ctx context.Context, job *DebugExportJob) {
	if job == nil {
		return
	}
	if err := s.ensureArtifactDir(); err != nil {
		s.markFailed(job.ID, err)
		return
	}
	if err := s.ensureDiskCapacity(ctx); err != nil {
		s.markFailed(job.ID, err)
		return
	}
	if s.handleExecutionContextDone(ctx, job.ID, "debug export worker stopped before collection") {
		return
	}
	if s.isJobCanceled(job.ID) {
		return
	}
	_ = s.repo.UpdateJobProgress(ctx, job.ID, 20, "collecting", 0)
	bundle, err := s.exporter.ExportWithOptions(ctx, job.Options)
	if err != nil {
		s.markFailed(job.ID, err)
		return
	}
	if s.handleExecutionContextDone(ctx, job.ID, "debug export worker stopped after collection") {
		return
	}
	if s.isJobCanceled(job.ID) {
		return
	}
	_ = s.repo.UpdateJobProgress(ctx, job.ID, 70, "writing", 0)
	fileName := fmt.Sprintf("sub2api-debug-export-%d-%s.json", job.ID, s.now().UTC().Format("20060102T150405Z"))
	relPath := filepath.ToSlash(fileName)
	finalPath, err := s.safeArtifactPath(relPath)
	if err != nil {
		s.markFailed(job.ID, err)
		return
	}
	tmpPath := finalPath + ".tmp"
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		s.markFailed(job.ID, err)
		return
	}
	hash := sha256.New()
	sized := &debugExportLimitWriter{writer: io.MultiWriter(file, hash), limit: debugExportMaxArtifactBytes}
	encoder := json.NewEncoder(sized)
	encoder.SetIndent("", "  ")
	err = encoder.Encode(bundle)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(tmpPath)
		if err == nil {
			err = closeErr
		}
		s.markFailed(job.ID, err)
		return
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		s.markFailed(job.ID, err)
		return
	}
	if info.Size() > debugExportMaxArtifactBytes {
		_ = os.Remove(tmpPath)
		s.markFailed(job.ID, errDebugExportArtifactTooLarge)
		return
	}
	if s.handleExecutionContextDone(ctx, job.ID, "debug export worker stopped while writing artifact") {
		_ = os.Remove(tmpPath)
		return
	}
	if s.isJobCanceled(job.ID) {
		_ = os.Remove(tmpPath)
		return
	}
	if err := s.ensureRetainedArtifactCapacity(ctx, info.Size()); err != nil {
		_ = os.Remove(tmpPath)
		s.markFailed(job.ID, err)
		return
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		s.markFailed(job.ID, err)
		return
	}
	if s.handleExecutionContextDone(ctx, job.ID, "debug export worker stopped after artifact rename") {
		_ = os.Remove(finalPath)
		return
	}
	if s.isJobCanceled(job.ID) {
		_ = os.Remove(finalPath)
		return
	}
	expiresAt := s.now().Add(debugExportArtifactRetain).UTC()
	sha := hex.EncodeToString(hash.Sum(nil))
	if err := s.repo.MarkJobSucceeded(context.Background(), job.ID, 100, "ready", info.Size(), fileName, relPath, info.Size(), sha, expiresAt); err != nil {
		_ = os.Remove(finalPath)
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] mark succeeded failed: job=%d err=%v", job.ID, err)
		if !errors.Is(err, sql.ErrNoRows) {
			s.markFailed(job.ID, err)
		}
	}
}

func (s *DebugExportJobService) handleExecutionContextDone(ctx context.Context, jobID int64, message string) bool {
	if ctx == nil || ctx.Err() == nil {
		return false
	}
	s.markFailed(jobID, fmt.Errorf("%s: %w", message, ctx.Err()))
	return true
}

func (s *DebugExportJobService) isJobCanceled(jobID int64) bool {
	status, err := s.repo.GetJobStatus(context.Background(), jobID)
	return err == nil && status == DebugExportJobStatusCanceled
}

func (s *DebugExportJobService) markFailed(jobID int64, err error) {
	msg := sanitizeDebugExportJobError(err)
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_ = s.repo.MarkJobFailed(context.Background(), jobID, msg)
}

func sanitizeDebugExportJobError(err error) string {
	if err == nil {
		return "debug export job failed"
	}
	if errors.Is(err, errDebugExportArtifactTooLarge) {
		return "debug export artifact exceeded the configured size limit"
	}
	msg := sanitizeSystemDebugExportText(err.Error())
	msg = strings.TrimSpace(logredact.RedactText(msg, systemDebugExportRedactKeys...))
	if msg == "" {
		return "debug export job failed"
	}
	return msg
}

func (s *DebugExportJobService) cleanupExpired() {
	if s == nil || s.repo == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	jobs, err := s.repo.ListExpiredSucceededJobs(ctx, s.now(), 50)
	if err != nil {
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] cleanup list failed: %v", err)
		return
	}
	for i := range jobs {
		if jobs[i].ArtifactPath != nil {
			if path, err := s.safeArtifactPath(*jobs[i].ArtifactPath); err == nil {
				_ = os.Remove(path)
			}
		}
		_ = s.repo.MarkJobExpired(ctx, jobs[i].ID)
	}
	s.removeTempFiles()
	s.removeOrphanArtifacts(ctx)
}

func (s *DebugExportJobService) ensureArtifactDir() error {
	if err := os.MkdirAll(s.baseDir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(s.baseDir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("debug export artifact directory is not a secure directory")
	}
	return os.Chmod(s.baseDir, 0700)
}

func (s *DebugExportJobService) ensureDiskCapacity(ctx context.Context) error {
	usage, err := disk.UsageWithContext(ctx, s.baseDir)
	if err != nil {
		return fmt.Errorf("check debug export artifact disk capacity: %w", err)
	}
	needed := uint64(debugExportMinFreeDiskBytes + debugExportMaxArtifactBytes)
	if usage.Free < needed {
		return infraerrors.ServiceUnavailable("DEBUG_EXPORT_INSUFFICIENT_DISK_SPACE", "insufficient free disk space for debug export artifact")
	}
	return nil
}

func (s *DebugExportJobService) safeArtifactPath(relPath string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relPath)))
	if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", infraerrors.BadRequest("DEBUG_EXPORT_INVALID_ARTIFACT_PATH", "invalid debug export artifact path")
	}
	base, err := filepath.Abs(s.baseDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, clean)
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if abs != base && !strings.HasPrefix(abs, base+string(filepath.Separator)) {
		return "", infraerrors.BadRequest("DEBUG_EXPORT_INVALID_ARTIFACT_PATH", "invalid debug export artifact path")
	}
	if evaluatedBase, err := filepath.EvalSymlinks(base); err == nil {
		candidateParent := filepath.Dir(abs)
		if evaluatedParent, err := filepath.EvalSymlinks(candidateParent); err == nil {
			if evaluatedParent != evaluatedBase && !strings.HasPrefix(evaluatedParent, evaluatedBase+string(filepath.Separator)) {
				return "", infraerrors.BadRequest("DEBUG_EXPORT_INVALID_ARTIFACT_PATH", "invalid debug export artifact path")
			}
		}
	}
	return abs, nil
}

func (s *DebugExportJobService) removeTempFiles() {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return
	}
	now := s.now()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			continue
		}
		path, err := s.safeArtifactPath(entry.Name())
		if err == nil && s.shouldRemoveStaleFile(path, now, debugExportTempArtifactGrace) {
			_ = os.Remove(path)
		}
	}
}

func (s *DebugExportJobService) removeOrphanArtifacts(ctx context.Context) {
	livePaths, err := s.repo.ListLiveArtifactPaths(ctx, s.now(), debugExportLiveArtifactPathLimit)
	if err != nil {
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] cleanup live artifact list failed: %v", err)
		return
	}
	if len(livePaths) >= debugExportLiveArtifactPathLimit {
		logger.LegacyPrintf("service.debug_export_job", "[DebugExportJob] skip orphan artifact cleanup: live path limit reached (%d)", debugExportLiveArtifactPathLimit)
		return
	}
	live := make(map[string]struct{}, len(livePaths))
	for _, rel := range livePaths {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel))))
		if clean != "." && clean != ".." {
			live[clean] = struct{}{}
		}
	}
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return
	}
	now := s.now()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, debugExportArtifactPrefix) || !strings.HasSuffix(name, debugExportArtifactSuffix) {
			continue
		}
		if _, ok := live[filepath.ToSlash(name)]; ok {
			continue
		}
		path, err := s.safeArtifactPath(name)
		if err == nil && s.shouldRemoveStaleFile(path, now, debugExportOrphanArtifactGrace) {
			_ = os.Remove(path)
		}
	}
}

func (s *DebugExportJobService) shouldRemoveStaleFile(path string, now time.Time, grace time.Duration) bool {
	info, err := os.Lstat(path)
	if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false
	}
	return info.ModTime().Add(grace).Before(now)
}

type debugExportLimitWriter struct {
	writer  io.Writer
	limit   int64
	written int64
}

func (w *debugExportLimitWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.limit {
		remaining := w.limit - w.written
		if remaining > 0 {
			n, _ := w.writer.Write(p[:remaining])
			w.written = w.limit
			return n, errDebugExportArtifactTooLarge
		}
		return 0, errDebugExportArtifactTooLarge
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}
