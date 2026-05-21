package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

type testSystemDebugOpsCounters struct{}

func (testSystemDebugOpsCounters) QueueLength() int64    { return 3 }
func (testSystemDebugOpsCounters) QueueCapacity() int    { return 128 }
func (testSystemDebugOpsCounters) DroppedTotal() int64   { return 5 }
func (testSystemDebugOpsCounters) EnqueuedTotal() int64  { return 13 }
func (testSystemDebugOpsCounters) ProcessedTotal() int64 { return 11 }
func (testSystemDebugOpsCounters) SanitizedTotal() int64 { return 7 }

type testSystemDebugOpsRepository struct {
	latest              *OpsSystemMetricsSnapshot
	latestErr           error
	heartbeats          []*OpsJobHeartbeat
	heartbeatsErr       error
	systemLogs          *OpsSystemLogList
	systemLogsErr       error
	errorLogs           *OpsErrorLogList
	errorLogsErr        error
	systemLogSampled    bool
	errorLogSampled     bool
	systemLogWindowFrom time.Time
	systemLogWindowTo   time.Time
	errorLogWindowFrom  time.Time
	errorLogWindowTo    time.Time
}

func (r *testSystemDebugOpsRepository) InsertErrorLog(context.Context, *OpsInsertErrorLogInput) (int64, error) {
	return 0, nil
}
func (r *testSystemDebugOpsRepository) BatchInsertErrorLogs(context.Context, []*OpsInsertErrorLogInput) (int64, error) {
	return 0, nil
}
func (r *testSystemDebugOpsRepository) ListErrorLogs(context.Context, *OpsErrorLogFilter) (*OpsErrorLogList, error) {
	return r.errorLogs, r.errorLogsErr
}
func (r *testSystemDebugOpsRepository) GetErrorLogByID(context.Context, int64) (*OpsErrorLogDetail, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) ListRequestDetails(context.Context, *OpsRequestDetailFilter) ([]*OpsRequestDetail, int64, error) {
	return nil, 0, nil
}
func (r *testSystemDebugOpsRepository) BatchInsertSystemLogs(context.Context, []*OpsInsertSystemLogInput) (int64, error) {
	return 0, nil
}
func (r *testSystemDebugOpsRepository) ListSystemLogs(context.Context, *OpsSystemLogFilter) (*OpsSystemLogList, error) {
	return r.systemLogs, r.systemLogsErr
}
func (r *testSystemDebugOpsRepository) SampleSystemLogsForDebugExport(_ context.Context, start, end time.Time, limit int) ([]*OpsSystemLog, bool, error) {
	r.systemLogSampled = true
	r.systemLogWindowFrom = start
	r.systemLogWindowTo = end
	if r.systemLogsErr != nil || r.systemLogs == nil {
		return nil, false, r.systemLogsErr
	}
	logs := r.systemLogs.Logs
	if limit <= 0 || len(logs) <= limit {
		return logs, false, nil
	}
	return logs[:limit], true, nil
}
func (r *testSystemDebugOpsRepository) SampleErrorLogsForDebugExport(_ context.Context, start, end time.Time, limit int) ([]*OpsErrorLog, bool, error) {
	r.errorLogSampled = true
	r.errorLogWindowFrom = start
	r.errorLogWindowTo = end
	if r.errorLogsErr != nil || r.errorLogs == nil {
		return nil, false, r.errorLogsErr
	}
	logs := r.errorLogs.Errors
	if limit <= 0 || len(logs) <= limit {
		return logs, false, nil
	}
	return logs[:limit], true, nil
}
func (r *testSystemDebugOpsRepository) DeleteSystemLogs(context.Context, *OpsSystemLogCleanupFilter) (int64, error) {
	return 0, nil
}
func (r *testSystemDebugOpsRepository) InsertSystemLogCleanupAudit(context.Context, *OpsSystemLogCleanupAudit) error {
	return nil
}
func (r *testSystemDebugOpsRepository) UpdateErrorResolution(context.Context, int64, bool, *int64, *time.Time) error {
	return nil
}
func (r *testSystemDebugOpsRepository) GetWindowStats(context.Context, *OpsDashboardFilter) (*OpsWindowStats, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetRealtimeTrafficSummary(context.Context, *OpsDashboardFilter) (*OpsRealtimeTrafficSummary, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetDashboardOverview(context.Context, *OpsDashboardFilter) (*OpsDashboardOverview, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetThroughputTrend(context.Context, *OpsDashboardFilter, int) (*OpsThroughputTrendResponse, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetLatencyHistogram(context.Context, *OpsDashboardFilter) (*OpsLatencyHistogramResponse, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetErrorTrend(context.Context, *OpsDashboardFilter, int) (*OpsErrorTrendResponse, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetErrorDistribution(context.Context, *OpsDashboardFilter) (*OpsErrorDistributionResponse, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetOpenAITokenStats(context.Context, *OpsOpenAITokenStatsFilter) (*OpsOpenAITokenStatsResponse, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) InsertSystemMetrics(context.Context, *OpsInsertSystemMetricsInput) error {
	return nil
}
func (r *testSystemDebugOpsRepository) GetLatestSystemMetrics(context.Context, int) (*OpsSystemMetricsSnapshot, error) {
	return r.latest, r.latestErr
}
func (r *testSystemDebugOpsRepository) UpsertJobHeartbeat(context.Context, *OpsUpsertJobHeartbeatInput) error {
	return nil
}
func (r *testSystemDebugOpsRepository) ListJobHeartbeats(_ context.Context, limit int) ([]*OpsJobHeartbeat, error) {
	if r.heartbeatsErr != nil || limit <= 0 || len(r.heartbeats) <= limit {
		return r.heartbeats, r.heartbeatsErr
	}
	return r.heartbeats[:limit], nil
}
func (r *testSystemDebugOpsRepository) ListAlertRules(context.Context) ([]*OpsAlertRule, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) CreateAlertRule(context.Context, *OpsAlertRule) (*OpsAlertRule, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) UpdateAlertRule(context.Context, *OpsAlertRule) (*OpsAlertRule, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) DeleteAlertRule(context.Context, int64) error { return nil }
func (r *testSystemDebugOpsRepository) ListAlertEvents(context.Context, *OpsAlertEventFilter) ([]*OpsAlertEvent, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetAlertEventByID(context.Context, int64) (*OpsAlertEvent, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetActiveAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) GetLatestAlertEvent(context.Context, int64) (*OpsAlertEvent, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) CreateAlertEvent(context.Context, *OpsAlertEvent) (*OpsAlertEvent, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) UpdateAlertEventStatus(context.Context, int64, string, *time.Time) error {
	return nil
}
func (r *testSystemDebugOpsRepository) UpdateAlertEventEmailSent(context.Context, int64, bool) error {
	return nil
}
func (r *testSystemDebugOpsRepository) CreateAlertSilence(context.Context, *OpsAlertSilence) (*OpsAlertSilence, error) {
	return nil, nil
}
func (r *testSystemDebugOpsRepository) IsAlertSilenced(context.Context, int64, string, *int64, *string, time.Time) (bool, error) {
	return false, nil
}
func (r *testSystemDebugOpsRepository) UpsertHourlyMetrics(context.Context, time.Time, time.Time) error {
	return nil
}
func (r *testSystemDebugOpsRepository) UpsertDailyMetrics(context.Context, time.Time, time.Time) error {
	return nil
}
func (r *testSystemDebugOpsRepository) GetLatestHourlyBucketStart(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (r *testSystemDebugOpsRepository) GetLatestDailyBucketDate(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func newSystemDebugExportTestClient(t *testing.T) *dbent.Client {
	t.Helper()

	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared&_fk=1", t.Name()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(drv)))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestSystemDebugExportRedactionCanary(t *testing.T) {
	cfg := &config.Config{
		RunMode:  config.RunModeSimple,
		Timezone: "UTC",
		Server: config.ServerConfig{
			Host:           "0.0.0.0",
			Port:           8080,
			Mode:           "release",
			FrontendURL:    "https://admin@example.com/path",
			TrustedProxies: []string{"10.0.0.1", "192.168.0.1"},
		},
		Database: config.DatabaseConfig{
			Host:     "db.internal",
			User:     "postgres",
			Password: "fake-db-password",
			DBName:   "sub2api",
			SSLMode:  "require",
		},
		Redis: config.RedisConfig{
			Host:     "redis.internal",
			Password: "fake-redis-password",
			DB:       2,
		},
		JWT: config.JWTConfig{
			Secret: "fake-jwt-secret",
		},
		Totp: config.TotpConfig{
			EncryptionKey: "fake-totp-key",
		},
		Default: config.DefaultConfig{
			AdminEmail:    "admin@example.com",
			AdminPassword: "fake-admin-password",
			APIKeyPrefix:  "sk-fake-prefix",
		},
		Gemini: config.GeminiConfig{
			OAuth: config.GeminiOAuthConfig{
				ClientID:     "fake-client-id",
				ClientSecret: "fake-client-secret",
			},
		},
	}

	svc := NewSystemDebugExportService(cfg, nil, nil, nil, nil, BuildInfo{Version: "v1.2.3", BuildType: "test"}, testSystemDebugOpsCounters{})
	svc.now = func() time.Time { return time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC) }

	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)
	require.Equal(t, systemDebugExportSchemaVersion, bundle.SchemaVersion)
	require.Equal(t, "2026-05-20T01:02:03Z", bundle.GeneratedAt)
	require.Equal(t, SystemDebugExportDetailStandard, bundle.Manifest.DetailLevel)
	require.Equal(t, SystemDebugExportSensitiveMasked, bundle.Manifest.SensitiveHandling)
	require.Equal(t, "***", bundle.Redaction.Marker)
	require.Equal(t, SystemDebugExportSensitiveMasked, bundle.Redaction.SensitiveHandling)
	require.Equal(t, 2, bundle.Configuration.Server.TrustedProxyCount)
	require.Equal(t, int64(5), bundle.Ops.ErrorLogQueue.DroppedTotal)
	require.Empty(t, bundle.Sensitive.Items)

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)

	for _, canary := range []string{
		"fake-db-password",
		"fake-redis-password",
		"fake-jwt-secret",
		"fake-totp-key",
		"fake-admin-password",
		"fake-client-secret",
		"admin@example.com",
		"db.internal",
		"redis.internal",
		"sk-fake-prefix",
	} {
		require.NotContains(t, text, canary)
	}
	require.Contains(t, text, "***")
	require.True(t, strings.Contains(text, "raw_config"))
}

func TestSystemDebugExportDiagnosticSensitiveModeAddsOnlyMetadata(t *testing.T) {
	cfg := &config.Config{
		RunMode:  config.RunModeStandard,
		Timezone: "UTC",
		Database: config.DatabaseConfig{
			Host:     "db.internal",
			User:     "postgres",
			Password: "fake-db-password",
			DBName:   "sub2api",
		},
		Redis: config.RedisConfig{
			Host:     "redis.internal",
			Password: "fake-redis-password",
		},
		JWT: config.JWTConfig{Secret: "fake-jwt-secret"},
		Totp: config.TotpConfig{
			EncryptionKey:           "fake-totp-key",
			EncryptionKeyConfigured: true,
		},
		Default: config.DefaultConfig{
			AdminEmail:    "admin@example.com",
			AdminPassword: "fake-admin-password",
			APIKeyPrefix:  "sk-fake-prefix",
		},
		Gemini: config.GeminiConfig{OAuth: config.GeminiOAuthConfig{
			ClientID:     "fake-client-id",
			ClientSecret: "fake-client-secret",
		}},
	}
	svc := NewSystemDebugExportService(cfg, nil, nil, nil, nil, BuildInfo{Version: "v1.2.3", BuildType: "test"}, testSystemDebugOpsCounters{})

	bundle, err := svc.ExportWithOptions(context.Background(), SystemDebugExportOptions{
		DetailLevel:       SystemDebugExportDetailSupport,
		SensitiveHandling: SystemDebugExportSensitiveDiagnostic,
	})
	require.NoError(t, err)
	require.Equal(t, SystemDebugExportDetailSupport, bundle.Manifest.DetailLevel)
	require.Equal(t, SystemDebugExportSensitiveDiagnostic, bundle.Manifest.SensitiveHandling)
	require.Equal(t, SystemDebugExportSensitiveDiagnostic, bundle.Sensitive.Handling)
	require.NotEmpty(t, bundle.Sensitive.Items)
	require.Equal(t, 50, bundle.Manifest.Limits.LogAttributionSamples)
	require.Equal(t, 72, bundle.Manifest.Limits.LogAttributionWindowHours)
	require.Equal(t, int64(4320), bundle.Manifest.Limits.LogAttributionWindowMinutes)
	require.Equal(t, SystemDebugExportLogWindowDetailDefault, bundle.LogAttribution.Window.Preset)

	itemsByName := map[string]SystemDebugExportSensitiveDiagnosticItem{}
	for _, item := range bundle.Sensitive.Items {
		itemsByName[item.ItemName] = item
	}
	databasePassword := itemsByName["database.password"]
	require.True(t, databasePassword.Configured)
	require.Equal(t, "9-16", databasePassword.LengthBucket)
	require.Empty(t, databasePassword.Fingerprint)
	require.Equal(t, "credential", databasePassword.FormatHint)
	totpKey := itemsByName["totp.encryption_value"]
	require.Contains(t, totpKey.Notes, "manual_configured=true")

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)
	for _, canary := range []string{
		"fake-db-password",
		"fake-redis-password",
		"fake-jwt-secret",
		"fake-totp-key",
		"fake-admin-password",
		"fake-client-secret",
		"admin@example.com",
		"db.internal",
		"redis.internal",
		"sk-fake-prefix",
		"sha256:",
	} {
		require.NotContains(t, text, canary)
	}
}

func TestNormalizeSystemDebugExportOptionsRejectsInvalidValues(t *testing.T) {
	_, err := NormalizeSystemDebugExportOptions(SystemDebugExportOptions{DetailLevel: "raw", SensitiveHandling: SystemDebugExportSensitiveMasked})
	require.Error(t, err)
	_, err = NormalizeSystemDebugExportOptions(SystemDebugExportOptions{DetailLevel: SystemDebugExportDetailStandard, SensitiveHandling: "plain"})
	require.Error(t, err)
	_, err = NormalizeSystemDebugExportOptions(SystemDebugExportOptions{DetailLevel: SystemDebugExportDetailStandard, SensitiveHandling: SystemDebugExportSensitiveMasked, LogWindowPreset: "2d"})
	require.Error(t, err)
	options, err := NormalizeSystemDebugExportOptions(SystemDebugExportOptions{DetailLevel: " SUPPORT ", SensitiveHandling: " DIAGNOSTIC ", LogWindowPreset: " 1D "})
	require.NoError(t, err)
	require.Equal(t, SystemDebugExportDetailSupport, options.DetailLevel)
	require.Equal(t, SystemDebugExportSensitiveDiagnostic, options.SensitiveHandling)
	require.Equal(t, SystemDebugExportLogWindowLast1Day, options.LogWindowPreset)
}

func TestResolveSystemDebugLogAttributionWindowPresetsAndCustom(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	window, err := resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{
		DetailLevel:       SystemDebugExportDetailSupport,
		SensitiveHandling: SystemDebugExportSensitiveMasked,
		LogWindowPreset:   SystemDebugExportLogWindowLast30Minutes,
	}, now)
	require.NoError(t, err)
	require.Equal(t, SystemDebugExportLogWindowLast30Minutes, window.Preset)
	require.Equal(t, int64(1800), window.WindowSeconds)
	require.Equal(t, int64(30), window.WindowMinutes)
	require.Equal(t, "2026-05-20T11:30:00Z", window.Start)
	require.Equal(t, "2026-05-20T12:00:00Z", window.End)

	window, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{
		DetailLevel:       SystemDebugExportDetailDetailed,
		SensitiveHandling: SystemDebugExportSensitiveMasked,
	}, now)
	require.NoError(t, err)
	require.Equal(t, SystemDebugExportLogWindowDetailDefault, window.Preset)
	require.Equal(t, int64(48*60*60), window.WindowSeconds)

	window, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{
		DetailLevel:       SystemDebugExportDetailSupport,
		SensitiveHandling: SystemDebugExportSensitiveMasked,
		LogWindowPreset:   SystemDebugExportLogWindowCustom,
		CustomLogStart:    "2026-05-20T09:00:00Z",
		CustomLogEnd:      "2026-05-20T10:30:00Z",
	}, now)
	require.NoError(t, err)
	require.Equal(t, SystemDebugExportLogWindowCustom, window.Preset)
	require.Equal(t, int64(90*60), window.WindowSeconds)

	_, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{LogWindowPreset: SystemDebugExportLogWindowCustom}, now)
	require.Error(t, err)
	_, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{LogWindowPreset: SystemDebugExportLogWindowCustom, CustomLogStart: "2026-05-20T10:00:00Z", CustomLogEnd: "2026-05-20T09:00:00Z"}, now)
	require.Error(t, err)
	_, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{LogWindowPreset: SystemDebugExportLogWindowCustom, CustomLogStart: "2026-05-12T10:00:00Z", CustomLogEnd: "2026-05-20T09:00:00Z"}, now)
	require.Error(t, err)
	_, err = resolveSystemDebugLogAttributionWindow(SystemDebugExportOptions{LogWindowPreset: SystemDebugExportLogWindowCustom, CustomLogStart: "2026-05-20T10:00:00Z", CustomLogEnd: "2026-05-20T13:00:00Z"}, now)
	require.Error(t, err)
}

func TestSystemDebugExportAccountSchedulingIncludesBlockedAndExcludesHealthyAccounts(t *testing.T) {
	client := newSystemDebugExportTestClient(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC)
	rejected := "rejected"
	blockedErr := "upstream failed access_token=secret-token"
	healthyErr := "healthy account secret should not appear"

	blocked, err := client.Account.Create().
		SetName("blocked account name should not export").
		SetNotes("blocked account notes should not export").
		SetPlatform("claude").
		SetType("oauth").
		SetCredentials(map[string]any{"access_token": "credential-secret"}).
		SetExtra(map[string]any{"raw": "extra-secret"}).
		SetStatus(StatusError).
		SetSchedulable(false).
		SetErrorMessage(blockedErr).
		SetRateLimitedAt(now.Add(-time.Minute)).
		SetRateLimitResetAt(now.Add(time.Hour)).
		SetOverloadUntil(now.Add(2 * time.Hour)).
		SetTempUnschedulableUntil(now.Add(3 * time.Hour)).
		SetTempUnschedulableReason("temporary password=reason-secret").
		SetSessionWindowStart(now.Add(-time.Hour)).
		SetSessionWindowEnd(now.Add(time.Hour)).
		SetSessionWindowStatus(rejected).
		SetExpiresAt(now.Add(-time.Hour)).
		SetAutoPauseOnExpired(true).
		SetLastUsedAt(now.Add(-2 * time.Hour)).
		Save(ctx)
	require.NoError(t, err)

	healthy, err := client.Account.Create().
		SetName("healthy account name should not export").
		SetPlatform("claude").
		SetType("oauth").
		SetStatus(StatusActive).
		SetSchedulable(true).
		SetErrorMessage(healthyErr).
		SetRateLimitResetAt(now.Add(-time.Hour)).
		SetOverloadUntil(now.Add(-time.Hour)).
		SetTempUnschedulableUntil(now.Add(-time.Hour)).
		SetExpiresAt(now.Add(time.Hour)).
		SetAutoPauseOnExpired(true).
		Save(ctx)
	require.NoError(t, err)

	svc := NewSystemDebugExportService(nil, client, nil, nil, nil, BuildInfo{}, nil)
	svc.now = func() time.Time { return now }
	bundle, err := svc.Export(ctx)
	require.NoError(t, err)

	section := bundle.AccountScheduling
	require.Equal(t, 50, section.SampleLimit)
	require.Equal(t, 1, section.MatchingCount)
	require.Equal(t, 1, section.SampleCount)
	require.False(t, section.Truncated)
	require.Empty(t, section.CollectionError)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: "claude", Count: 1}}, section.Summary.ByPlatform)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: "oauth", Count: 1}}, section.Summary.ByType)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: StatusError, Count: 1}}, section.Summary.ByStatus)
	require.Equal(t, 1, section.BlockerCounts["status_error"])
	require.Equal(t, 1, section.BlockerCounts["schedulable_false"])
	require.Equal(t, 1, section.BlockerCounts["rate_limited"])
	require.Equal(t, 1, section.BlockerCounts["overloaded"])
	require.Equal(t, 1, section.BlockerCounts["temp_unschedulable"])
	require.Equal(t, 1, section.BlockerCounts["session_rejected"])
	require.Equal(t, 1, section.BlockerCounts["expired_auto_paused"])

	require.Len(t, section.Samples, 1)
	sample := section.Samples[0]
	require.Equal(t, blocked.ID, sample.AccountID)
	require.NotEqual(t, healthy.ID, sample.AccountID)
	require.Equal(t, []string{"status_error", "schedulable_false", "rate_limited", "overloaded", "temp_unschedulable", "session_rejected", "expired_auto_paused"}, sample.Blockers)
	require.Contains(t, sample.ErrorMessage, "access_token=***")
	require.Contains(t, sample.TempUnschedulableReason, "password=***")

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)
	for _, forbidden := range []string{
		"blocked account name should not export",
		"blocked account notes should not export",
		"healthy account name should not export",
		"healthy account secret should not appear",
		"credential-secret",
		"extra-secret",
		"access_token=secret-token",
		"password=reason-secret",
	} {
		require.NotContains(t, text, forbidden)
	}
	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	accountScheduling, ok := generic["account_scheduling"].(map[string]any)
	require.True(t, ok)
	samples, ok := accountScheduling["samples"].([]any)
	require.True(t, ok)
	require.Len(t, samples, 1)
	sampleMap, ok := samples[0].(map[string]any)
	require.True(t, ok)
	for _, forbiddenKey := range []string{"name", "notes", "credentials", "extra", "proxy_id", "group_id", "api_key", "user_email", "user_ip"} {
		require.NotContains(t, sampleMap, forbiddenKey)
	}
}

func TestSystemDebugExportAccountSchedulingCapsSamplesAndTruncatesText(t *testing.T) {
	client := newSystemDebugExportTestClient(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC)
	longMessage := strings.Repeat("界", 300)

	for i := 0; i < 55; i++ {
		_, err := client.Account.Create().
			SetName(fmt.Sprintf("blocked-%02d", i)).
			SetPlatform("openai").
			SetType("api_key").
			SetStatus(StatusActive).
			SetSchedulable(false).
			SetErrorMessage(longMessage).
			Save(ctx)
		require.NoError(t, err)
	}

	svc := NewSystemDebugExportService(nil, client, nil, nil, nil, BuildInfo{}, nil)
	svc.now = func() time.Time { return now }
	bundle, err := svc.Export(ctx)
	require.NoError(t, err)

	section := bundle.AccountScheduling
	require.Equal(t, 50, section.SampleLimit)
	require.Equal(t, 55, section.MatchingCount)
	require.Equal(t, 50, section.SampleCount)
	require.True(t, section.Truncated)
	require.Len(t, section.Samples, 50)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: "openai", Count: 55}}, section.Summary.ByPlatform)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: "api_key", Count: 55}}, section.Summary.ByType)
	require.Equal(t, []SystemDebugExportAccountSchedulingCount{{Value: StatusActive, Count: 55}}, section.Summary.ByStatus)
	require.Equal(t, 55, section.BlockerCounts["schedulable_false"])
	require.Len(t, []rune(section.Samples[0].ErrorMessage), 256)
	require.Equal(t, strings.Repeat("界", 256), section.Samples[0].ErrorMessage)
}

func TestSystemDebugExportAccountSchedulingNilEntClientSetsCollectionError(t *testing.T) {
	svc := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{}, nil)
	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)

	section := bundle.AccountScheduling
	require.Equal(t, 50, section.SampleLimit)
	require.Zero(t, section.MatchingCount)
	require.Zero(t, section.SampleCount)
	require.Empty(t, section.Samples)
	require.NotEmpty(t, section.CollectionError)
	require.LessOrEqual(t, len([]rune(section.CollectionError)), 256)
}

func TestSystemDebugExportServerConditionsNilDependenciesExportUnavailable(t *testing.T) {
	repo := &testSystemDebugOpsRepository{
		latestErr:     errors.New("dial tcp db.internal:5432: connection refused password=secret"),
		heartbeatsErr: context.DeadlineExceeded,
	}
	svc := NewSystemDebugExportService(nil, nil, repo, nil, nil, BuildInfo{}, nil)
	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)

	conditions := bundle.ServerConditions
	require.Equal(t, "partial", conditions.Status)
	require.NotEmpty(t, conditions.CollectedAt)
	require.Equal(t, "unavailable", conditions.Database.Status)
	require.Equal(t, "unavailable", conditions.Database.ErrorKind)
	require.Equal(t, "unavailable", conditions.Redis.Status)
	require.Equal(t, "unavailable", conditions.Redis.ErrorKind)
	require.Equal(t, "unavailable", conditions.LatestOpsMetrics.Status)
	require.Equal(t, "connection_failed", conditions.LatestOpsMetrics.ErrorKind)
	require.Equal(t, "unavailable", conditions.OpsJobHeartbeats.Status)
	require.Equal(t, "timeout", conditions.OpsJobHeartbeats.ErrorKind)
	require.Empty(t, conditions.OpsJobHeartbeats.Heartbeats)

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)
	require.Contains(t, text, "server_conditions")
	for _, forbidden := range []string{"db.internal", "5432", "password=secret", "connection refused"} {
		require.NotContains(t, text, forbidden)
	}
}

func TestSystemDebugExportServerConditionsIncludesSafeOpsMetricsAndHeartbeats(t *testing.T) {
	cpuUsage := 12.5
	memoryUsed := int64(512)
	memoryTotal := int64(2048)
	memoryPercent := 25.0
	dbOK := true
	redisOK := false
	dbMaxOpen := 20
	redisPoolSize := 30
	redisTotal := 7
	redisIdle := 3
	dbActive := 4
	dbIdle := 6
	dbWaiting := 2
	goroutines := 99
	queueDepth := 8
	accountSwitches := int64(42)
	now := time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC)
	lastError := strings.Repeat("x", 300) + " api_key=super-secret-token"
	lastResult := "result with token should not export"
	repo := &testSystemDebugOpsRepository{
		latest: &OpsSystemMetricsSnapshot{
			ID:                    12345,
			CreatedAt:             now.Add(-time.Minute),
			WindowMinutes:         1,
			CPUUsagePercent:       &cpuUsage,
			MemoryUsedMB:          &memoryUsed,
			MemoryTotalMB:         &memoryTotal,
			MemoryUsagePercent:    &memoryPercent,
			DBOK:                  &dbOK,
			RedisOK:               &redisOK,
			DBMaxOpenConns:        &dbMaxOpen,
			RedisPoolSize:         &redisPoolSize,
			RedisConnTotal:        &redisTotal,
			RedisConnIdle:         &redisIdle,
			DBConnActive:          &dbActive,
			DBConnIdle:            &dbIdle,
			DBConnWaiting:         &dbWaiting,
			GoroutineCount:        &goroutines,
			ConcurrencyQueueDepth: &queueDepth,
			AccountSwitchCount:    &accountSwitches,
		},
		heartbeats: []*OpsJobHeartbeat{
			{JobName: "z-cleanup", LastResult: &lastResult, UpdatedAt: now},
			{JobName: "a-metrics", LastRunAt: &now, LastErrorAt: &now, LastError: &lastError, UpdatedAt: now.Add(time.Second)},
		},
	}
	svc := NewSystemDebugExportService(nil, nil, repo, nil, nil, BuildInfo{}, nil)
	svc.now = func() time.Time { return now }
	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)

	snapshot := bundle.ServerConditions.LatestOpsMetrics.Snapshot
	require.NotNil(t, snapshot)
	require.Equal(t, now.Add(-time.Minute), snapshot.CreatedAt)
	require.Equal(t, 1, snapshot.WindowMinutes)
	require.Equal(t, &cpuUsage, snapshot.CPUUsagePercent)
	require.Equal(t, &memoryUsed, snapshot.MemoryUsedMB)
	require.Equal(t, &memoryTotal, snapshot.MemoryTotalMB)
	require.Equal(t, &memoryPercent, snapshot.MemoryUsagePercent)
	require.Equal(t, &dbOK, snapshot.DBOK)
	require.Equal(t, &redisOK, snapshot.RedisOK)
	require.Equal(t, &dbMaxOpen, snapshot.DBMaxOpenConns)
	require.Equal(t, &redisPoolSize, snapshot.RedisPoolSize)
	require.Equal(t, &redisTotal, snapshot.RedisConnTotal)
	require.Equal(t, &redisIdle, snapshot.RedisConnIdle)
	require.Equal(t, &dbActive, snapshot.DBConnActive)
	require.Equal(t, &dbIdle, snapshot.DBConnIdle)
	require.Equal(t, &dbWaiting, snapshot.DBConnWaiting)
	require.Equal(t, &goroutines, snapshot.GoroutineCount)
	require.Equal(t, &queueDepth, snapshot.ConcurrencyQueueDepth)
	require.Equal(t, &accountSwitches, snapshot.AccountSwitchCount)

	heartbeats := bundle.ServerConditions.OpsJobHeartbeats
	require.Equal(t, 20, heartbeats.Limit)
	require.Equal(t, 2, heartbeats.Count)
	require.False(t, heartbeats.Truncated)
	require.Equal(t, "a-metrics", heartbeats.Heartbeats[0].JobName)
	require.Equal(t, "z-cleanup", heartbeats.Heartbeats[1].JobName)
	require.Len(t, []rune(heartbeats.Heartbeats[0].LastError), 256)
	require.NotContains(t, heartbeats.Heartbeats[0].LastError, "super-secret-token")

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)
	require.NotContains(t, text, "12345")
	require.NotContains(t, text, "last_result")
	require.NotContains(t, text, lastResult)
	require.NotContains(t, text, "super-secret-token")
}

func TestSystemDebugExportLogAttributionSummarizesIndexedLogsSafely(t *testing.T) {
	now := time.Date(2026, 5, 20, 1, 2, 3, 0, time.UTC)
	repo := &testSystemDebugOpsRepository{
		systemLogs: &OpsSystemLogList{
			Total:    2,
			Page:     1,
			PageSize: 20,
			Logs: []*OpsSystemLog{
				{
					CreatedAt:       now.Add(-time.Minute),
					Level:           "error",
					Component:       "handler.openai_gateway.responses",
					Message:         "panic for user admin@example.com ip=192.168.1.1 token=secret-token Bearer abcdef123456 sk-testsecret123456",
					RequestID:       "req-1",
					ClientRequestID: "client-1",
					Platform:        "openai",
					Model:           "gpt-test",
				},
				{
					CreatedAt: now.Add(-2 * time.Minute),
					Level:     "warn",
					Component: "handler.openai_gateway.responses",
					Message:   "upstream timeout password=raw-password",
				},
			},
		},
		errorLogs: &OpsErrorLogList{
			Total:    1,
			Page:     1,
			PageSize: 20,
			Errors: []*OpsErrorLog{
				{
					CreatedAt:        now.Add(-30 * time.Second),
					RequestID:        "req-1",
					ClientRequestID:  "client-1",
					Phase:            "upstream",
					Type:             "upstream_error",
					Owner:            "provider",
					Source:           "upstream_http",
					Severity:         "error",
					StatusCode:       500,
					Platform:         "openai",
					Model:            "gpt-test",
					RequestPath:      "/v1/responses",
					InboundEndpoint:  "/v1/responses",
					UpstreamEndpoint: "/v1/responses",
					Message:          "provider failed user=admin@example.com request_body={secret} api_key=hidden",
					UserEmail:        "admin@example.com",
					AccountName:      "account name should not export",
				},
			},
		},
	}
	svc := NewSystemDebugExportService(nil, nil, repo, nil, nil, BuildInfo{}, nil)
	svc.now = func() time.Time { return now }

	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)

	section := bundle.LogAttribution
	require.True(t, repo.systemLogSampled)
	require.True(t, repo.errorLogSampled)
	require.Equal(t, now.Add(-24*time.Hour), repo.systemLogWindowFrom)
	require.Equal(t, now, repo.systemLogWindowTo)
	require.Equal(t, now.Add(-24*time.Hour), repo.errorLogWindowFrom)
	require.Equal(t, now, repo.errorLogWindowTo)
	require.Equal(t, "ok", section.Status)
	require.Equal(t, 24, section.WindowHours)
	require.Equal(t, SystemDebugExportLogWindowDetailDefault, section.Window.Preset)
	require.Equal(t, int64(86400), section.Window.WindowSeconds)
	require.Equal(t, 20, section.Limit)
	require.Contains(t, section.Capabilities, "correlate_by_request_id_and_client_request_id")
	require.Contains(t, section.Limitations, "raw_logs_request_bodies_headers_error_bodies_and_full_stacks_are_not_exported")
	require.False(t, section.SystemLogs.TotalCountExact)
	require.Equal(t, 2, section.SystemLogs.SampleCount)
	require.Equal(t, []SystemDebugExportLogAttributionCount{{Value: "handler.openai_gateway.responses", Count: 2}}, section.SystemLogs.ByComponent)
	require.Equal(t, []SystemDebugExportLogAttributionCount{{Value: "error", Count: 1}, {Value: "warn", Count: 1}}, section.SystemLogs.ByLevel)
	require.False(t, section.ErrorLogs.TotalCountExact)
	require.Equal(t, 1, section.ErrorLogs.SampleCount)
	require.Equal(t, []SystemDebugExportLogAttributionCount{{Value: "upstream", Count: 1}}, section.ErrorLogs.ByPhase)
	require.Equal(t, []SystemDebugExportLogAttributionCount{{Value: "500", Count: 1}}, section.ErrorLogs.ByStatus)
	require.NotEmpty(t, section.DiagnosticHints)
	require.Contains(t, section.SystemLogs.Samples[0].MessageExcerpt, "panic for user *** ip=***")
	require.Contains(t, section.ErrorLogs.Samples[0].MessageExcerpt, "api_key=***")

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	text := string(raw)
	for _, forbidden := range []string{
		"admin@example.com",
		"192.168.1.1",
		"secret-token",
		"abcdef123456",
		"sk-testsecret123456",
		"raw-password",
		"request_body={secret}",
		"account name should not export",
		"user_email",
		"account_name",
	} {
		require.NotContains(t, text, forbidden)
	}
}

func TestSystemDebugExportDatabaseProbeStatsDoNotExposeConnectionSecrets(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(17)
	mock.ExpectPing()

	svc := NewSystemDebugExportService(nil, nil, nil, db, nil, BuildInfo{}, nil)
	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	database := bundle.ServerConditions.Database
	require.Equal(t, "ok", database.Status)
	require.NotNil(t, database.LatencyMs)
	require.Equal(t, 17, database.Stats.MaxOpen)

	raw, err := json.Marshal(bundle)
	require.NoError(t, err)
	databaseRaw, err := json.Marshal(database)
	require.NoError(t, err)
	databaseText := string(databaseRaw)
	text := string(raw)
	require.Contains(t, text, "open_connections")
	for _, forbidden := range []string{"dsn", "host", "user", "password", "db.internal", "postgres://", "connection_string"} {
		require.NotContains(t, databaseText, forbidden)
	}
}

func TestSystemDebugExportServerConditionsSerializedTypeShape(t *testing.T) {
	svc := NewSystemDebugExportService(nil, nil, nil, nil, nil, BuildInfo{}, nil)
	bundle, err := svc.Export(context.Background())
	require.NoError(t, err)
	raw, err := json.Marshal(bundle)
	require.NoError(t, err)

	var generic map[string]any
	require.NoError(t, json.Unmarshal(raw, &generic))
	serverConditions, ok := generic["server_conditions"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"status", "collected_at", "cpu", "memory", "disk", "host", "process", "database", "redis", "latest_ops_metrics", "ops_job_heartbeats"} {
		require.Contains(t, serverConditions, key)
	}
	diskSection, ok := serverConditions["disk"].(map[string]any)
	require.True(t, ok)
	volumes, ok := diskSection["volumes"].([]any)
	require.True(t, ok)
	if len(volumes) > 0 {
		volume, ok := volumes[0].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "root", volume["label"])
		for _, forbiddenKey := range []string{"path", "mountpoint", "device", "serial"} {
			require.NotContains(t, volume, forbiddenKey)
		}
	}
}
