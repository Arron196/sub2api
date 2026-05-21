package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type testDebugExportOpsCounters struct{}

func (testDebugExportOpsCounters) QueueLength() int64    { return 1 }
func (testDebugExportOpsCounters) QueueCapacity() int    { return 32 }
func (testDebugExportOpsCounters) DroppedTotal() int64   { return 2 }
func (testDebugExportOpsCounters) EnqueuedTotal() int64  { return 8 }
func (testDebugExportOpsCounters) ProcessedTotal() int64 { return 6 }
func (testDebugExportOpsCounters) SanitizedTotal() int64 { return 4 }

func TestSystemHandlerDebugExportReturnsDownloadableBundle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	debugExportSvc := service.NewSystemDebugExportService(
		&config.Config{RunMode: config.RunModeStandard, Timezone: "UTC"},
		nil,
		nil,
		nil,
		nil,
		service.BuildInfo{Version: "v9.9.9", BuildType: "test"},
		testDebugExportOpsCounters{},
	)
	handler := NewSystemHandler(nil, nil, debugExportSvc, nil)

	router := gin.New()
	router.POST("/debug-export", handler.DebugExport)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/debug-export", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Equal(t, "nosniff", recorder.Header().Get("X-Content-Type-Options"))
	require.Contains(t, recorder.Header().Get("Content-Disposition"), "sub2api-debug-export-")
	require.True(t, strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json"))

	var bundle service.SystemDebugExportBundle
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &bundle))
	require.Equal(t, "system.debug_export.v1", bundle.SchemaVersion)
	require.Equal(t, "v9.9.9", bundle.System.Version)
	require.Equal(t, int64(2), bundle.Ops.ErrorLogQueue.DroppedTotal)
	require.Equal(t, service.SystemDebugExportDetailStandard, bundle.Manifest.DetailLevel)
	require.Equal(t, service.SystemDebugExportSensitiveMasked, bundle.Redaction.SensitiveHandling)
	require.Equal(t, service.SystemDebugExportLogWindowDetailDefault, bundle.LogAttribution.Window.Preset)
}

func TestSystemHandlerDebugExportAcceptsOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	debugExportSvc := service.NewSystemDebugExportService(
		&config.Config{RunMode: config.RunModeStandard, Timezone: "UTC"},
		nil,
		nil,
		nil,
		nil,
		service.BuildInfo{Version: "v9.9.9", BuildType: "test"},
		testDebugExportOpsCounters{},
	)
	handler := NewSystemHandler(nil, nil, debugExportSvc, nil)

	router := gin.New()
	router.POST("/debug-export", handler.DebugExport)

	recorder := httptest.NewRecorder()
	body := []byte(`{"detail_level":"support","sensitive_handling":"diagnostic","log_window_preset":"30m"}`)
	request := httptest.NewRequest(http.MethodPost, "/debug-export", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	var bundle service.SystemDebugExportBundle
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &bundle))
	require.Equal(t, service.SystemDebugExportDetailSupport, bundle.Manifest.DetailLevel)
	require.Equal(t, service.SystemDebugExportSensitiveDiagnostic, bundle.Manifest.SensitiveHandling)
	require.Equal(t, service.SystemDebugExportSensitiveDiagnostic, bundle.Redaction.SensitiveHandling)
	require.Equal(t, service.SystemDebugExportLogWindowLast30Minutes, bundle.LogAttribution.Window.Preset)
	require.Equal(t, int64(1800), bundle.LogAttribution.Window.WindowSeconds)
}

func TestSystemHandlerDebugExportRejectsInvalidOptions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	debugExportSvc := service.NewSystemDebugExportService(
		&config.Config{RunMode: config.RunModeStandard, Timezone: "UTC"},
		nil,
		nil,
		nil,
		nil,
		service.BuildInfo{Version: "v9.9.9", BuildType: "test"},
		testDebugExportOpsCounters{},
	)
	handler := NewSystemHandler(nil, nil, debugExportSvc, nil)
	router := gin.New()
	router.POST("/debug-export", handler.DebugExport)

	recorder := httptest.NewRecorder()
	body := []byte(`{"detail_level":"raw","sensitive_handling":"diagnostic"}`)
	request := httptest.NewRequest(http.MethodPost, "/debug-export", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}

func TestSystemHandlerDebugExportRejectsOversizedOptionsBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	debugExportSvc := service.NewSystemDebugExportService(
		&config.Config{RunMode: config.RunModeStandard, Timezone: "UTC"},
		nil,
		nil,
		nil,
		nil,
		service.BuildInfo{Version: "v9.9.9", BuildType: "test"},
		testDebugExportOpsCounters{},
	)
	handler := NewSystemHandler(nil, nil, debugExportSvc, nil)
	router := gin.New()
	router.POST("/debug-export", handler.DebugExport)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/debug-export", strings.NewReader(`{"detail_level":"support","padding":"`+strings.Repeat("x", debugExportOptionsMaxBodyBytes)+`"}`))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
}

func TestSystemHandlerDebugExportRejectsInvalidCustomLogWindow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	debugExportSvc := service.NewSystemDebugExportService(
		&config.Config{RunMode: config.RunModeStandard, Timezone: "UTC"},
		nil,
		nil,
		nil,
		nil,
		service.BuildInfo{Version: "v9.9.9", BuildType: "test"},
		testDebugExportOpsCounters{},
	)
	handler := NewSystemHandler(nil, nil, debugExportSvc, nil)
	router := gin.New()
	router.POST("/debug-export", handler.DebugExport)

	recorder := httptest.NewRecorder()
	body := []byte(`{"detail_level":"support","sensitive_handling":"masked","log_window_preset":"custom","custom_log_start":"2026-05-20T12:00:00Z","custom_log_end":"2026-05-20T11:00:00Z"}`)
	request := httptest.NewRequest(http.MethodPost, "/debug-export", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
