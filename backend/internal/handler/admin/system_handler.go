package admin

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/sysutil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

const debugExportOptionsMaxBodyBytes = 4096

// SystemHandler handles system-related operations
type SystemHandler struct {
	updateSvc      *service.UpdateService
	lockSvc        *service.SystemOperationLockService
	debugExportSvc *service.SystemDebugExportService
	debugJobSvc    *service.DebugExportJobService
}

type debugExportRequest struct {
	DetailLevel       string `json:"detail_level"`
	SensitiveHandling string `json:"sensitive_handling"`
	LogWindowPreset   string `json:"log_window_preset"`
	CustomLogStart    string `json:"custom_log_start"`
	CustomLogEnd      string `json:"custom_log_end"`
}

// NewSystemHandler creates a new SystemHandler
func NewSystemHandler(updateSvc *service.UpdateService, lockSvc *service.SystemOperationLockService, debugExportSvc *service.SystemDebugExportService, debugJobSvc *service.DebugExportJobService) *SystemHandler {
	return &SystemHandler{
		updateSvc:      updateSvc,
		lockSvc:        lockSvc,
		debugExportSvc: debugExportSvc,
		debugJobSvc:    debugJobSvc,
	}
}

// GetVersion returns the current version
// GET /api/v1/admin/system/version
func (h *SystemHandler) GetVersion(c *gin.Context) {
	info, _ := h.updateSvc.CheckUpdate(c.Request.Context(), false)
	response.Success(c, gin.H{
		"version": info.CurrentVersion,
	})
}

// CheckUpdates checks for available updates
// GET /api/v1/admin/system/check-updates
func (h *SystemHandler) CheckUpdates(c *gin.Context) {
	force := c.Query("force") == "true"
	info, err := h.updateSvc.CheckUpdate(c.Request.Context(), force)
	if err != nil {
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	response.Success(c, info)
}

// PerformUpdate downloads and applies the update
// POST /api/v1/admin/system/update
func (h *SystemHandler) PerformUpdate(c *gin.Context) {
	operationID := buildSystemOperationID(c, "update")
	payload := gin.H{"operation_id": operationID}
	executeAdminIdempotentJSON(c, "admin.system.update", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		var releaseReason string
		succeeded := false
		defer func() {
			release(releaseReason, succeeded)
		}()

		if err := h.updateSvc.PerformUpdate(ctx); err != nil {
			releaseReason = "SYSTEM_UPDATE_FAILED"
			return nil, err
		}
		succeeded = true

		return gin.H{
			"message":      "Update completed. Please restart the service.",
			"need_restart": true,
			"operation_id": lock.OperationID(),
		}, nil
	})
}

// Rollback restores the previous version
// POST /api/v1/admin/system/rollback
func (h *SystemHandler) Rollback(c *gin.Context) {
	operationID := buildSystemOperationID(c, "rollback")
	payload := gin.H{"operation_id": operationID}
	executeAdminIdempotentJSON(c, "admin.system.rollback", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		var releaseReason string
		succeeded := false
		defer func() {
			release(releaseReason, succeeded)
		}()

		if err := h.updateSvc.Rollback(); err != nil {
			releaseReason = "SYSTEM_ROLLBACK_FAILED"
			return nil, err
		}
		succeeded = true

		return gin.H{
			"message":      "Rollback completed. Please restart the service.",
			"need_restart": true,
			"operation_id": lock.OperationID(),
		}, nil
	})
}

// RestartService restarts the systemd service
// POST /api/v1/admin/system/restart
func (h *SystemHandler) RestartService(c *gin.Context) {
	operationID := buildSystemOperationID(c, "restart")
	payload := gin.H{"operation_id": operationID}
	executeAdminIdempotentJSON(c, "admin.system.restart", payload, service.DefaultSystemOperationIdempotencyTTL(), func(ctx context.Context) (any, error) {
		lock, release, err := h.acquireSystemLock(ctx, operationID)
		if err != nil {
			return nil, err
		}
		succeeded := false
		defer func() {
			release("", succeeded)
		}()

		// Schedule service restart in background after sending response
		// This ensures the client receives the success response before the service restarts
		go func() {
			// Wait a moment to ensure the response is sent
			time.Sleep(500 * time.Millisecond)
			sysutil.RestartServiceAsync()
		}()
		succeeded = true
		return gin.H{
			"message":      "Service restart initiated",
			"operation_id": lock.OperationID(),
		}, nil
	})
}

// DebugExport returns a redacted, allowlisted system debug bundle.
// POST /api/v1/admin/system/debug-export
func (h *SystemHandler) DebugExport(c *gin.Context) {
	if h.debugExportSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export service unavailable")
		return
	}
	var req debugExportRequest
	if c.Request.ContentLength != 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, debugExportOptionsMaxBodyBytes)
		if err := c.ShouldBindJSON(&req); err != nil {
			if isMaxBytesError(err) {
				response.Error(c, http.StatusRequestEntityTooLarge, "debug export options too large")
				return
			}
			response.Error(c, http.StatusBadRequest, "invalid debug export options")
			return
		}
	}
	options, err := service.NormalizeSystemDebugExportOptions(service.SystemDebugExportOptions{
		DetailLevel:       req.DetailLevel,
		SensitiveHandling: req.SensitiveHandling,
		LogWindowPreset:   req.LogWindowPreset,
		CustomLogStart:    req.CustomLogStart,
		CustomLogEnd:      req.CustomLogEnd,
	})
	if err != nil {
		response.Error(c, http.StatusBadRequest, err.Error())
		return
	}
	bundle, err := h.debugExportSvc.ExportWithOptions(c.Request.Context(), options)
	if err != nil {
		if strings.HasPrefix(err.Error(), "invalid debug export custom log window") {
			response.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		response.Error(c, http.StatusInternalServerError, err.Error())
		return
	}
	filename := "sub2api-debug-export-" + time.Now().UTC().Format("20060102T150405Z") + ".json"
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.JSON(http.StatusOK, bundle)
}

// CreateDebugExportJob creates a durable async debug export job.
// POST /api/v1/admin/system/debug-export-jobs
func (h *SystemHandler) CreateDebugExportJob(c *gin.Context) {
	if h.debugJobSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export job service unavailable")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}
	var req debugExportRequest
	if c.Request.ContentLength != 0 {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, debugExportOptionsMaxBodyBytes)
		if err := c.ShouldBindJSON(&req); err != nil {
			if isMaxBytesError(err) {
				response.Error(c, http.StatusRequestEntityTooLarge, "debug export options too large")
				return
			}
			response.Error(c, http.StatusBadRequest, "invalid debug export options")
			return
		}
	}
	job, err := h.debugJobSvc.CreateJob(c.Request.Context(), service.SystemDebugExportOptions{
		DetailLevel:       req.DetailLevel,
		SensitiveHandling: req.SensitiveHandling,
		LogWindowPreset:   req.LogWindowPreset,
		CustomLogStart:    req.CustomLogStart,
		CustomLogEnd:      req.CustomLogEnd,
	}, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Accepted(c, job)
}

// ListDebugExportJobs lists recent async debug export jobs.
// GET /api/v1/admin/system/debug-export-jobs
func (h *SystemHandler) ListDebugExportJobs(c *gin.Context) {
	if h.debugJobSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export job service unavailable")
		return
	}
	jobs, err := h.debugJobSvc.ListRecentJobs(c.Request.Context(), 20)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if jobs == nil {
		jobs = []service.DebugExportJob{}
	}
	response.Success(c, gin.H{"items": jobs})
}

// GetDebugExportJob returns one async debug export job.
// GET /api/v1/admin/system/debug-export-jobs/:id
func (h *SystemHandler) GetDebugExportJob(c *gin.Context) {
	if h.debugJobSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export job service unavailable")
		return
	}
	jobID, ok := parseDebugExportJobID(c)
	if !ok {
		return
	}
	job, err := h.debugJobSvc.GetJob(c.Request.Context(), jobID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}

// CancelDebugExportJob cancels a pending/running async debug export job.
// POST /api/v1/admin/system/debug-export-jobs/:id/cancel
func (h *SystemHandler) CancelDebugExportJob(c *gin.Context) {
	if h.debugJobSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export job service unavailable")
		return
	}
	jobID, ok := parseDebugExportJobID(c)
	if !ok {
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "Unauthorized")
		return
	}
	if err := h.debugJobSvc.CancelJob(c.Request.Context(), jobID, subject.UserID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	job, err := h.debugJobSvc.GetJob(c.Request.Context(), jobID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}

// DownloadDebugExportJob streams a succeeded, unexpired async debug export artifact.
// GET /api/v1/admin/system/debug-export-jobs/:id/download
func (h *SystemHandler) DownloadDebugExportJob(c *gin.Context) {
	if h.debugJobSvc == nil {
		response.Error(c, http.StatusInternalServerError, "debug export job service unavailable")
		return
	}
	jobID, ok := parseDebugExportJobID(c)
	if !ok {
		return
	}
	download, err := h.debugJobSvc.OpenDownload(c.Request.Context(), jobID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	defer func() { _ = download.Reader.Close() }()
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.FileName}))
	c.DataFromReader(http.StatusOK, download.SizeBytes, download.ContentType, download.Reader, nil)
}

func parseDebugExportJobID(c *gin.Context) (int64, bool) {
	jobID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || jobID <= 0 {
		response.BadRequest(c, "invalid debug export job ID")
		return 0, false
	}
	return jobID, true
}

func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func (h *SystemHandler) acquireSystemLock(
	ctx context.Context,
	operationID string,
) (*service.SystemOperationLock, func(string, bool), error) {
	if h.lockSvc == nil {
		return nil, nil, service.ErrIdempotencyStoreUnavail
	}
	lock, err := h.lockSvc.Acquire(ctx, operationID)
	if err != nil {
		return nil, nil, err
	}
	release := func(reason string, succeeded bool) {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.lockSvc.Release(releaseCtx, lock, succeeded, reason)
	}
	return lock, release, nil
}

func buildSystemOperationID(c *gin.Context, operation string) string {
	key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if key == "" {
		return "sysop-" + operation + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	actorScope := "admin:0"
	if subject, ok := middleware2.GetAuthSubjectFromContext(c); ok {
		actorScope = "admin:" + strconv.FormatInt(subject.UserID, 10)
	}
	seed := operation + "|" + actorScope + "|" + c.FullPath() + "|" + key
	hash := service.HashIdempotencyKey(seed)
	if len(hash) > 24 {
		hash = hash[:24]
	}
	return "sysop-" + hash
}
