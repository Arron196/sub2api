package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/account"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

const systemDebugExportSchemaVersion = "system.debug_export.v1"
const systemDebugExportAccountSchedulingSampleLimit = 50
const systemDebugExportAccountSchedulingTimeout = 2 * time.Second
const systemDebugExportTimeout = 5 * time.Second
const systemDebugExportProbeTimeout = 500 * time.Millisecond
const systemDebugExportJobHeartbeatLimit = 20
const systemDebugExportLogAttributionLimit = 20
const systemDebugExportLogAttributionWindow = 24 * time.Hour
const systemDebugExportMaxCustomLogAttributionWindow = 7 * 24 * time.Hour

const (
	SystemDebugExportDetailStandard = "standard"
	SystemDebugExportDetailDetailed = "detailed"
	SystemDebugExportDetailSupport  = "support"
)

const (
	SystemDebugExportSensitiveMasked     = "masked"
	SystemDebugExportSensitiveDiagnostic = "diagnostic"
)

const (
	SystemDebugExportLogWindowLast30Minutes = "30m"
	SystemDebugExportLogWindowLast6Hours    = "6h"
	SystemDebugExportLogWindowLast1Day      = "1d"
	SystemDebugExportLogWindowLast3Days     = "3d"
	SystemDebugExportLogWindowLast1Week     = "1w"
	SystemDebugExportLogWindowCustom        = "custom"
	SystemDebugExportLogWindowDetailDefault = "detail_default"
)

var systemDebugExportRedactKeys = []string{
	"api_key",
	"apiKey",
	"apikey",
	"auth_header",
	"body",
	"cookie",
	"email",
	"environment",
	"env",
	"headers",
	"ip",
	"jwt_secret",
	"key",
	"payment_config",
	"prompt",
	"proxy_password",
	"request_body",
	"response_body",
	"secret",
	"settings",
	"token",
}

var (
	systemDebugLogEmailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	systemDebugLogIPv4Pattern   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	systemDebugLogBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/\-=]+`)
	systemDebugLogSKPattern     = regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{8,}\b`)
)

type SystemDebugOpsCountersProvider interface {
	QueueLength() int64
	QueueCapacity() int
	DroppedTotal() int64
	EnqueuedTotal() int64
	ProcessedTotal() int64
	SanitizedTotal() int64
}

type SystemDebugExportService struct {
	cfg         *config.Config
	entClient   *ent.Client
	opsRepo     OpsRepository
	db          *sql.DB
	redisClient *redis.Client
	buildInfo   BuildInfo
	opsCounters SystemDebugOpsCountersProvider
	now         func() time.Time
}

type SystemDebugExportOptions struct {
	DetailLevel       string `json:"detail_level"`
	SensitiveHandling string `json:"sensitive_handling"`
	LogWindowPreset   string `json:"log_window_preset"`
	CustomLogStart    string `json:"custom_log_start,omitempty"`
	CustomLogEnd      string `json:"custom_log_end,omitempty"`
}

type SystemDebugExportBundle struct {
	SchemaVersion     string                             `json:"schema_version"`
	GeneratedAt       string                             `json:"generated_at"`
	Manifest          SystemDebugExportManifest          `json:"manifest"`
	Redaction         SystemDebugExportRedaction         `json:"redaction"`
	System            SystemDebugExportSystem            `json:"system"`
	Runtime           SystemDebugExportRuntime           `json:"runtime"`
	ServerConditions  SystemDebugExportServerConditions  `json:"server_conditions"`
	Configuration     SystemDebugExportConfiguration     `json:"configuration"`
	Ops               SystemDebugExportOps               `json:"ops"`
	LogAttribution    SystemDebugExportLogAttribution    `json:"log_attribution"`
	Sensitive         SystemDebugExportSensitive         `json:"sensitive_diagnostics"`
	AccountScheduling SystemDebugExportAccountScheduling `json:"account_scheduling"`
}

type SystemDebugExportManifest struct {
	DetailLevel       string                            `json:"detail_level"`
	SensitiveHandling string                            `json:"sensitive_handling"`
	GeneratedFor      string                            `json:"generated_for"`
	IncludedSections  []string                          `json:"included_sections"`
	SafetyNotes       []string                          `json:"safety_notes"`
	Limits            SystemDebugExportManifestLimits   `json:"limits"`
	Timeouts          SystemDebugExportManifestTimeouts `json:"timeouts"`
}

type SystemDebugExportManifestLimits struct {
	AccountSchedulingSamples       int   `json:"account_scheduling_samples"`
	LogAttributionSamples          int   `json:"log_attribution_samples"`
	JobHeartbeatSamples            int   `json:"job_heartbeat_samples"`
	LogAttributionWindowHours      int   `json:"log_attribution_window_hours"`
	LogAttributionWindowMinutes    int64 `json:"log_attribution_window_minutes"`
	LogAttributionWindowSeconds    int64 `json:"log_attribution_window_seconds"`
	MaxLogAttributionWindowSeconds int64 `json:"max_log_attribution_window_seconds"`
}

type SystemDebugExportManifestTimeouts struct {
	ExportSeconds            int `json:"export_seconds"`
	ProbeMilliseconds        int `json:"probe_milliseconds"`
	AccountSchedulingSeconds int `json:"account_scheduling_seconds"`
}

type SystemDebugExportRedaction struct {
	Mode              string   `json:"mode"`
	SensitiveHandling string   `json:"sensitive_handling"`
	Marker            string   `json:"marker"`
	FinalPass         string   `json:"final_pass"`
	ExcludedSections  []string `json:"excluded_sections"`
}

type SystemDebugExportSensitive struct {
	Status   string                                     `json:"status"`
	Handling string                                     `json:"handling"`
	Notices  []string                                   `json:"notices"`
	Items    []SystemDebugExportSensitiveDiagnosticItem `json:"items"`
}

type SystemDebugExportSensitiveDiagnosticItem struct {
	ItemName      string   `json:"item_name"`
	ValueCategory string   `json:"value_category"`
	Configured    bool     `json:"configured"`
	LengthBucket  string   `json:"length_bucket,omitempty"`
	Fingerprint   string   `json:"fingerprint,omitempty"`
	FormatHint    string   `json:"format_hint,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type SystemDebugExportSystem struct {
	Version   string `json:"version"`
	BuildType string `json:"build_type"`
	RunMode   string `json:"run_mode"`
	Timezone  string `json:"timezone"`
}

type SystemDebugExportRuntime struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	GoVersion     string `json:"go_version"`
	NumCPU        int    `json:"num_cpu"`
	Goroutines    int    `json:"goroutines"`
	GOMAXPROCS    int    `json:"gomaxprocs"`
	MemoryAlloc   uint64 `json:"memory_alloc_bytes"`
	MemorySys     uint64 `json:"memory_sys_bytes"`
	MemoryHeapSys uint64 `json:"memory_heap_sys_bytes"`
}

type SystemDebugExportSectionStatus struct {
	Status    string `json:"status"`
	ErrorKind string `json:"error_kind,omitempty"`
}

type SystemDebugExportServerConditions struct {
	Status           string                                      `json:"status"`
	CollectedAt      string                                      `json:"collected_at"`
	CPU              SystemDebugExportCPUConditions              `json:"cpu"`
	Memory           SystemDebugExportMemoryConditions           `json:"memory"`
	Disk             SystemDebugExportDiskConditions             `json:"disk"`
	Host             SystemDebugExportHostConditions             `json:"host"`
	Process          SystemDebugExportProcessConditions          `json:"process"`
	Database         SystemDebugExportDatabaseConditions         `json:"database"`
	Redis            SystemDebugExportRedisConditions            `json:"redis"`
	LatestOpsMetrics SystemDebugExportLatestOpsMetricsConditions `json:"latest_ops_metrics"`
	OpsJobHeartbeats SystemDebugExportOpsJobHeartbeatConditions  `json:"ops_job_heartbeats"`
}

type SystemDebugExportCPUConditions struct {
	SystemDebugExportSectionStatus
	LogicalCount *int     `json:"logical_count,omitempty"`
	Percent      *float64 `json:"percent,omitempty"`
	Load1        *float64 `json:"load1,omitempty"`
	Load5        *float64 `json:"load5,omitempty"`
	Load15       *float64 `json:"load15,omitempty"`
}

type SystemDebugExportMemoryConditions struct {
	SystemDebugExportSectionStatus
	TotalBytes     *uint64  `json:"total_bytes,omitempty"`
	AvailableBytes *uint64  `json:"available_bytes,omitempty"`
	UsedBytes      *uint64  `json:"used_bytes,omitempty"`
	UsedPercent    *float64 `json:"used_percent,omitempty"`
}

type SystemDebugExportDiskConditions struct {
	SystemDebugExportSectionStatus
	Volumes []SystemDebugExportDiskVolumeConditions `json:"volumes"`
}

type SystemDebugExportDiskVolumeConditions struct {
	Label             string   `json:"label"`
	FSType            string   `json:"fstype,omitempty"`
	TotalBytes        uint64   `json:"total_bytes"`
	FreeBytes         uint64   `json:"free_bytes"`
	UsedBytes         uint64   `json:"used_bytes"`
	UsedPercent       float64  `json:"used_percent"`
	InodesUsedPercent *float64 `json:"inodes_used_percent,omitempty"`
}

type SystemDebugExportHostConditions struct {
	SystemDebugExportSectionStatus
	UptimeSeconds *uint64 `json:"uptime_seconds,omitempty"`
}

type SystemDebugExportProcessConditions struct {
	SystemDebugExportSectionStatus
	RSSBytes   *uint64  `json:"rss_bytes,omitempty"`
	VMSBytes   *uint64  `json:"vms_bytes,omitempty"`
	CPUPercent *float64 `json:"cpu_percent,omitempty"`
	NumThreads *int32   `json:"num_threads,omitempty"`
	NumFDs     *int32   `json:"num_fds,omitempty"`
}

type SystemDebugExportDatabaseConditions struct {
	SystemDebugExportSectionStatus
	LatencyMs *int64                                   `json:"latency_ms,omitempty"`
	Stats     SystemDebugExportDatabaseConnectionStats `json:"stats"`
}

type SystemDebugExportDatabaseConnectionStats struct {
	OpenConnections int   `json:"open_connections"`
	InUse           int   `json:"in_use"`
	Idle            int   `json:"idle"`
	WaitCount       int64 `json:"wait_count"`
	WaitDurationMs  int64 `json:"wait_duration_ms"`
	MaxOpen         int   `json:"max_open"`
}

type SystemDebugExportRedisConditions struct {
	SystemDebugExportSectionStatus
	LatencyMs *int64                          `json:"latency_ms,omitempty"`
	Pool      SystemDebugExportRedisPoolStats `json:"pool"`
}

type SystemDebugExportRedisPoolStats struct {
	Total    int64 `json:"total"`
	Idle     int64 `json:"idle"`
	Stale    int64 `json:"stale"`
	Hits     int64 `json:"hits"`
	Misses   int64 `json:"misses"`
	Timeouts int64 `json:"timeouts"`
}

type SystemDebugExportLatestOpsMetricsConditions struct {
	SystemDebugExportSectionStatus
	Snapshot *SystemDebugExportLatestOpsMetricsSnapshot `json:"snapshot,omitempty"`
}

type SystemDebugExportLatestOpsMetricsSnapshot struct {
	CreatedAt             time.Time `json:"created_at"`
	WindowMinutes         int       `json:"window_minutes"`
	CPUUsagePercent       *float64  `json:"cpu_usage_percent,omitempty"`
	MemoryUsedMB          *int64    `json:"memory_used_mb,omitempty"`
	MemoryTotalMB         *int64    `json:"memory_total_mb,omitempty"`
	MemoryUsagePercent    *float64  `json:"memory_usage_percent,omitempty"`
	DBOK                  *bool     `json:"db_ok,omitempty"`
	RedisOK               *bool     `json:"redis_ok,omitempty"`
	DBMaxOpenConns        *int      `json:"db_max_open_conns,omitempty"`
	RedisPoolSize         *int      `json:"redis_pool_size,omitempty"`
	RedisConnTotal        *int      `json:"redis_conn_total,omitempty"`
	RedisConnIdle         *int      `json:"redis_conn_idle,omitempty"`
	DBConnActive          *int      `json:"db_conn_active,omitempty"`
	DBConnIdle            *int      `json:"db_conn_idle,omitempty"`
	DBConnWaiting         *int      `json:"db_conn_waiting,omitempty"`
	GoroutineCount        *int      `json:"goroutine_count,omitempty"`
	ConcurrencyQueueDepth *int      `json:"concurrency_queue_depth,omitempty"`
	AccountSwitchCount    *int64    `json:"account_switch_count,omitempty"`
}

type SystemDebugExportOpsJobHeartbeatConditions struct {
	SystemDebugExportSectionStatus
	Limit      int                                      `json:"limit"`
	Count      int                                      `json:"count"`
	Truncated  bool                                     `json:"truncated"`
	Heartbeats []SystemDebugExportOpsJobHeartbeatSample `json:"heartbeats"`
}

type SystemDebugExportOpsJobHeartbeatSample struct {
	JobName        string     `json:"job_name"`
	LastRunAt      *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at,omitempty"`
	LastErrorAt    *time.Time `json:"last_error_at,omitempty"`
	LastDurationMs *int64     `json:"last_duration_ms,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastError      string     `json:"last_error,omitempty"`
}

type SystemDebugExportConfiguration struct {
	Server      SystemDebugExportServerConfig      `json:"server"`
	Database    SystemDebugExportDatabaseConfig    `json:"database"`
	Redis       SystemDebugExportRedisConfig       `json:"redis"`
	Security    SystemDebugExportSecurityConfig    `json:"security"`
	Gateway     SystemDebugExportGatewayConfig     `json:"gateway"`
	Ops         SystemDebugExportOpsConfig         `json:"ops"`
	RateLimit   SystemDebugExportRateLimitConfig   `json:"rate_limit"`
	Concurrency SystemDebugExportConcurrencyConfig `json:"concurrency"`
}

type SystemDebugExportServerConfig struct {
	Mode                   string `json:"mode"`
	ReadHeaderTimeout      int    `json:"read_header_timeout_seconds"`
	IdleTimeout            int    `json:"idle_timeout_seconds"`
	TrustedProxyCount      int    `json:"trusted_proxy_count"`
	MaxRequestBodySize     int64  `json:"max_request_body_size"`
	H2CEnabled             bool   `json:"h2c_enabled"`
	H2CMaxConcurrentStream uint32 `json:"h2c_max_concurrent_streams"`
}

type SystemDebugExportDatabaseConfig struct {
	SSLMode                string `json:"ssl_mode"`
	MaxOpenConns           int    `json:"max_open_conns"`
	MaxIdleConns           int    `json:"max_idle_conns"`
	ConnMaxLifetimeMinutes int    `json:"conn_max_lifetime_minutes"`
	ConnMaxIdleTimeMinutes int    `json:"conn_max_idle_time_minutes"`
}

type SystemDebugExportRedisConfig struct {
	DB                  int  `json:"db"`
	DialTimeoutSeconds  int  `json:"dial_timeout_seconds"`
	ReadTimeoutSeconds  int  `json:"read_timeout_seconds"`
	WriteTimeoutSeconds int  `json:"write_timeout_seconds"`
	PoolSize            int  `json:"pool_size"`
	MinIdleConns        int  `json:"min_idle_conns"`
	TLSEnabled          bool `json:"tls_enabled"`
}

type SystemDebugExportSecurityConfig struct {
	URLAllowlistEnabled        bool `json:"url_allowlist_enabled"`
	UpstreamHostCount          int  `json:"upstream_host_count"`
	PricingHostCount           int  `json:"pricing_host_count"`
	CRSHostCount               int  `json:"crs_host_count"`
	AllowPrivateHosts          bool `json:"allow_private_hosts"`
	AllowInsecureHTTP          bool `json:"allow_insecure_http"`
	ResponseHeadersEnabled     bool `json:"response_headers_enabled"`
	AdditionalAllowedHeaders   int  `json:"additional_allowed_headers_count"`
	ForceRemovedHeaders        int  `json:"force_removed_headers_count"`
	CSPEnabled                 bool `json:"csp_enabled"`
	ProxyFallbackDirectOnError bool `json:"proxy_fallback_direct_on_error"`
}

type SystemDebugExportGatewayConfig struct {
	ResponseHeaderTimeoutSeconds     int    `json:"response_header_timeout_seconds"`
	MaxBodySize                      int64  `json:"max_body_size"`
	UpstreamResponseReadMaxBytes     int64  `json:"upstream_response_read_max_bytes"`
	ProxyProbeResponseReadMaxBytes   int64  `json:"proxy_probe_response_read_max_bytes"`
	ConnectionPoolIsolation          string `json:"connection_pool_isolation"`
	MaxIdleConns                     int    `json:"max_idle_conns"`
	MaxIdleConnsPerHost              int    `json:"max_idle_conns_per_host"`
	MaxConnsPerHost                  int    `json:"max_conns_per_host"`
	IdleConnTimeoutSeconds           int    `json:"idle_conn_timeout_seconds"`
	MaxUpstreamClients               int    `json:"max_upstream_clients"`
	ClientIdleTTLSeconds             int    `json:"client_idle_ttl_seconds"`
	ConcurrencySlotTTLMinutes        int    `json:"concurrency_slot_ttl_minutes"`
	SessionIdleTimeoutMinutes        int    `json:"session_idle_timeout_minutes"`
	StreamDataIntervalTimeoutSeconds int    `json:"stream_data_interval_timeout_seconds"`
	StreamKeepaliveIntervalSeconds   int    `json:"stream_keepalive_interval_seconds"`
	ImageConcurrencyEnabled          bool   `json:"image_concurrency_enabled"`
	ImageConcurrencyMaxConcurrent    int    `json:"image_concurrency_max_concurrent_requests"`
	ImageConcurrencyOverflowMode     string `json:"image_concurrency_overflow_mode"`
	OpenAIWSEnabled                  bool   `json:"openai_ws_enabled"`
	UserMessageQueueMode             string `json:"user_message_queue_mode"`
}

type SystemDebugExportOpsConfig struct {
	Enabled                        bool   `json:"enabled"`
	UsePreaggregatedTables         bool   `json:"use_preaggregated_tables"`
	CleanupEnabled                 bool   `json:"cleanup_enabled"`
	CleanupSchedule                string `json:"cleanup_schedule"`
	ErrorLogRetentionDays          int    `json:"error_log_retention_days"`
	MinuteMetricsRetentionDays     int    `json:"minute_metrics_retention_days"`
	HourlyMetricsRetentionDays     int    `json:"hourly_metrics_retention_days"`
	MetricsCollectorCacheEnabled   bool   `json:"metrics_collector_cache_enabled"`
	MetricsCollectorCacheTTLString string `json:"metrics_collector_cache_ttl"`
	AggregationEnabled             bool   `json:"aggregation_enabled"`
}

type SystemDebugExportRateLimitConfig struct {
	OverloadCooldownMinutes int `json:"overload_cooldown_minutes"`
	OAuth401CooldownMinutes int `json:"oauth_401_cooldown_minutes"`
}

type SystemDebugExportConcurrencyConfig struct {
	PingIntervalSeconds int `json:"ping_interval_seconds"`
}

type SystemDebugExportOps struct {
	ErrorLogQueue SystemDebugExportOpsErrorLogQueue `json:"error_log_queue"`
}

type SystemDebugExportOpsErrorLogQueue struct {
	Length         int64 `json:"length"`
	Capacity       int   `json:"capacity"`
	DroppedTotal   int64 `json:"dropped_total"`
	EnqueuedTotal  int64 `json:"enqueued_total"`
	ProcessedTotal int64 `json:"processed_total"`
	SanitizedTotal int64 `json:"sanitized_total"`
}

type SystemDebugExportLogAttribution struct {
	SystemDebugExportSectionStatus
	Window          SystemDebugExportLogAttributionWindow           `json:"window"`
	WindowHours     int                                             `json:"window_hours"`
	Limit           int                                             `json:"limit"`
	Capabilities    []string                                        `json:"capabilities"`
	Limitations     []string                                        `json:"limitations"`
	SystemLogs      SystemDebugExportLogAttributionSystemLogs       `json:"system_logs"`
	ErrorLogs       SystemDebugExportLogAttributionErrorLogs        `json:"error_logs"`
	DiagnosticHints []SystemDebugExportLogAttributionDiagnosticHint `json:"diagnostic_hints"`
}

type SystemDebugExportLogAttributionWindow struct {
	Preset           string `json:"preset"`
	Start            string `json:"start"`
	End              string `json:"end"`
	WindowSeconds    int64  `json:"window_seconds"`
	WindowMinutes    int64  `json:"window_minutes"`
	MaxWindowSeconds int64  `json:"max_window_seconds"`
}

type SystemDebugExportLogAttributionSystemLogs struct {
	SystemDebugExportSectionStatus
	TotalCount      int                                           `json:"total_count"`
	TotalCountExact bool                                          `json:"total_count_exact"`
	SampleCount     int                                           `json:"sample_count"`
	Truncated       bool                                          `json:"truncated"`
	ByLevel         []SystemDebugExportLogAttributionCount        `json:"by_level"`
	ByComponent     []SystemDebugExportLogAttributionCount        `json:"by_component"`
	Samples         []SystemDebugExportLogAttributionSystemSample `json:"samples"`
}

type SystemDebugExportLogAttributionErrorLogs struct {
	SystemDebugExportSectionStatus
	TotalCount      int                                          `json:"total_count"`
	TotalCountExact bool                                         `json:"total_count_exact"`
	SampleCount     int                                          `json:"sample_count"`
	Truncated       bool                                         `json:"truncated"`
	ByPhase         []SystemDebugExportLogAttributionCount       `json:"by_phase"`
	ByType          []SystemDebugExportLogAttributionCount       `json:"by_type"`
	ByOwner         []SystemDebugExportLogAttributionCount       `json:"by_owner"`
	BySource        []SystemDebugExportLogAttributionCount       `json:"by_source"`
	ByStatus        []SystemDebugExportLogAttributionCount       `json:"by_status"`
	Samples         []SystemDebugExportLogAttributionErrorSample `json:"samples"`
}

type SystemDebugExportLogAttributionCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type SystemDebugExportLogAttributionSystemSample struct {
	CreatedAt       time.Time `json:"created_at"`
	Level           string    `json:"level"`
	Component       string    `json:"component"`
	RequestID       string    `json:"request_id,omitempty"`
	ClientRequestID string    `json:"client_request_id,omitempty"`
	Platform        string    `json:"platform,omitempty"`
	Model           string    `json:"model,omitempty"`
	MessageExcerpt  string    `json:"message_excerpt,omitempty"`
}

type SystemDebugExportLogAttributionErrorSample struct {
	CreatedAt        time.Time `json:"created_at"`
	RequestID        string    `json:"request_id,omitempty"`
	ClientRequestID  string    `json:"client_request_id,omitempty"`
	Phase            string    `json:"phase,omitempty"`
	Type             string    `json:"type,omitempty"`
	Owner            string    `json:"owner,omitempty"`
	Source           string    `json:"source,omitempty"`
	Severity         string    `json:"severity,omitempty"`
	StatusCode       int       `json:"status_code,omitempty"`
	Platform         string    `json:"platform,omitempty"`
	Model            string    `json:"model,omitempty"`
	RequestPath      string    `json:"request_path,omitempty"`
	InboundEndpoint  string    `json:"inbound_endpoint,omitempty"`
	UpstreamEndpoint string    `json:"upstream_endpoint,omitempty"`
	MessageExcerpt   string    `json:"message_excerpt,omitempty"`
}

type SystemDebugExportLogAttributionDiagnosticHint struct {
	Kind    string `json:"kind"`
	Summary string `json:"summary"`
}

type SystemDebugExportAccountScheduling struct {
	SampleLimit     int                                        `json:"sample_limit"`
	MatchingCount   int                                        `json:"matching_count"`
	SampleCount     int                                        `json:"sample_count"`
	Truncated       bool                                       `json:"truncated"`
	CollectionError string                                     `json:"collection_error,omitempty"`
	Summary         SystemDebugExportAccountSchedulingSummary  `json:"summary"`
	BlockerCounts   map[string]int                             `json:"blocker_counts"`
	Samples         []SystemDebugExportAccountSchedulingSample `json:"samples"`
}

type SystemDebugExportAccountSchedulingSummary struct {
	ByPlatform []SystemDebugExportAccountSchedulingCount `json:"by_platform"`
	ByType     []SystemDebugExportAccountSchedulingCount `json:"by_type"`
	ByStatus   []SystemDebugExportAccountSchedulingCount `json:"by_status"`
}

type SystemDebugExportAccountSchedulingCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type SystemDebugExportAccountSchedulingSample struct {
	AccountID               int64      `json:"account_id"`
	Platform                string     `json:"platform"`
	Type                    string     `json:"type"`
	Status                  string     `json:"status"`
	Schedulable             bool       `json:"schedulable"`
	LastUsedAt              *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt               *time.Time `json:"expires_at,omitempty"`
	RateLimitedAt           *time.Time `json:"rate_limited_at,omitempty"`
	RateLimitResetAt        *time.Time `json:"rate_limit_reset_at,omitempty"`
	OverloadUntil           *time.Time `json:"overload_until,omitempty"`
	TempUnschedulableUntil  *time.Time `json:"temp_unschedulable_until,omitempty"`
	SessionWindowStart      *time.Time `json:"session_window_start,omitempty"`
	SessionWindowEnd        *time.Time `json:"session_window_end,omitempty"`
	SessionWindowStatus     string     `json:"session_window_status,omitempty"`
	Blockers                []string   `json:"blockers"`
	ErrorMessage            string     `json:"error_message,omitempty"`
	TempUnschedulableReason string     `json:"temp_unschedulable_reason,omitempty"`
}

func NewSystemDebugExportService(cfg *config.Config, entClient *ent.Client, opsRepo OpsRepository, db *sql.DB, redisClient *redis.Client, buildInfo BuildInfo, opsCounters SystemDebugOpsCountersProvider) *SystemDebugExportService {
	return &SystemDebugExportService{
		cfg:         cfg,
		entClient:   entClient,
		opsRepo:     opsRepo,
		db:          db,
		redisClient: redisClient,
		buildInfo:   buildInfo,
		opsCounters: opsCounters,
		now:         time.Now,
	}
}

func (s *SystemDebugExportService) Export(ctx context.Context) (SystemDebugExportBundle, error) {
	return s.ExportWithOptions(ctx, SystemDebugExportOptions{})
}

func (s *SystemDebugExportService) ExportWithOptions(ctx context.Context, opts SystemDebugExportOptions) (SystemDebugExportBundle, error) {
	options, err := NormalizeSystemDebugExportOptions(opts)
	if err != nil {
		return SystemDebugExportBundle{}, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, systemDebugExportTimeout)
		defer cancel()
	}
	select {
	case <-ctx.Done():
		return SystemDebugExportBundle{}, ctx.Err()
	default:
	}

	now := s.now().UTC()
	logWindow, err := resolveSystemDebugLogAttributionWindow(options, now)
	if err != nil {
		return SystemDebugExportBundle{}, err
	}

	bundle := s.collect(ctx, options, now, logWindow)
	return redactSystemDebugExportBundle(bundle)
}

func (s *SystemDebugExportService) collect(ctx context.Context, opts SystemDebugExportOptions, now time.Time, logWindow SystemDebugExportLogAttributionWindow) SystemDebugExportBundle {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	logLimit := systemDebugExportLogAttributionLimitForDetail(opts.DetailLevel)

	return SystemDebugExportBundle{
		SchemaVersion: systemDebugExportSchemaVersion,
		GeneratedAt:   now.Format(time.RFC3339),
		Manifest:      buildSystemDebugExportManifest(opts, logLimit, logWindow),
		Redaction: SystemDebugExportRedaction{
			Mode:              "allowlisted-dto-only",
			SensitiveHandling: opts.SensitiveHandling,
			Marker:            "***",
			FinalPass:         "logredact",
			ExcludedSections: []string{
				"raw_config",
				"raw_settings",
				"entities",
				"credentials",
				"payment_config_maps",
				"proxy_passwords",
				"channel_monitor_payloads",
				"request_response_bodies",
				"logs",
				"pii",
			},
		},
		System: SystemDebugExportSystem{
			Version:   s.buildInfo.Version,
			BuildType: s.buildInfo.BuildType,
			RunMode:   safeRunMode(s.cfg),
			Timezone:  safeTimezone(s.cfg),
		},
		Runtime: SystemDebugExportRuntime{
			GOOS:          runtime.GOOS,
			GOARCH:        runtime.GOARCH,
			GoVersion:     runtime.Version(),
			NumCPU:        runtime.NumCPU(),
			Goroutines:    runtime.NumGoroutine(),
			GOMAXPROCS:    runtime.GOMAXPROCS(0),
			MemoryAlloc:   mem.Alloc,
			MemorySys:     mem.Sys,
			MemoryHeapSys: mem.HeapSys,
		},
		ServerConditions:  s.collectServerConditions(ctx, now),
		Configuration:     s.collectConfiguration(),
		Ops:               s.collectOps(),
		LogAttribution:    s.collectLogAttribution(ctx, logWindow, logLimit),
		Sensitive:         s.collectSensitiveDiagnostics(opts),
		AccountScheduling: s.collectAccountScheduling(ctx, now),
	}
}

func (s *SystemDebugExportService) collectServerConditions(ctx context.Context, now time.Time) SystemDebugExportServerConditions {
	conditions := SystemDebugExportServerConditions{
		Status:           "ok",
		CollectedAt:      now.Format(time.RFC3339),
		CPU:              s.collectCPUConditions(ctx),
		Memory:           s.collectMemoryConditions(ctx),
		Disk:             s.collectDiskConditions(ctx),
		Host:             s.collectHostConditions(ctx),
		Process:          s.collectProcessConditions(ctx),
		Database:         s.collectDatabaseConditions(ctx),
		Redis:            s.collectRedisConditions(ctx),
		LatestOpsMetrics: s.collectLatestOpsMetrics(ctx),
		OpsJobHeartbeats: s.collectOpsJobHeartbeats(ctx),
	}
	for _, status := range []string{
		conditions.CPU.Status,
		conditions.Memory.Status,
		conditions.Disk.Status,
		conditions.Host.Status,
		conditions.Process.Status,
		conditions.Database.Status,
		conditions.Redis.Status,
		conditions.LatestOpsMetrics.Status,
		conditions.OpsJobHeartbeats.Status,
	} {
		if status == "partial" || status == "unavailable" {
			conditions.Status = "partial"
			break
		}
	}
	return conditions
}

func (s *SystemDebugExportService) collectCPUConditions(ctx context.Context) SystemDebugExportCPUConditions {
	result := SystemDebugExportCPUConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	if count, err := cpu.CountsWithContext(probeCtx, true); err == nil {
		result.LogicalCount = &count
	} else {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	if values, err := cpu.PercentWithContext(probeCtx, 0, false); err == nil && len(values) > 0 {
		result.Percent = &values[0]
	} else if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	if avg, err := load.AvgWithContext(probeCtx); err == nil && avg != nil {
		result.Load1 = &avg.Load1
		result.Load5 = &avg.Load5
		result.Load15 = &avg.Load15
	} else if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	return result
}

func (s *SystemDebugExportService) collectMemoryConditions(ctx context.Context) SystemDebugExportMemoryConditions {
	result := SystemDebugExportMemoryConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	vm, err := mem.VirtualMemoryWithContext(probeCtx)
	if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
		return result
	}
	result.TotalBytes = &vm.Total
	result.AvailableBytes = &vm.Available
	result.UsedBytes = &vm.Used
	result.UsedPercent = &vm.UsedPercent
	return result
}

func (s *SystemDebugExportService) collectDiskConditions(ctx context.Context) SystemDebugExportDiskConditions {
	result := SystemDebugExportDiskConditions{
		SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"},
		Volumes:                        []SystemDebugExportDiskVolumeConditions{},
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	usage, err := disk.UsageWithContext(probeCtx, systemDebugExportRootPath())
	if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
		return result
	}
	volume := SystemDebugExportDiskVolumeConditions{
		Label:       "root",
		FSType:      usage.Fstype,
		TotalBytes:  usage.Total,
		FreeBytes:   usage.Free,
		UsedBytes:   usage.Used,
		UsedPercent: usage.UsedPercent,
	}
	if usage.InodesUsedPercent != 0 {
		volume.InodesUsedPercent = &usage.InodesUsedPercent
	}
	result.Volumes = append(result.Volumes, volume)
	return result
}

func (s *SystemDebugExportService) collectHostConditions(ctx context.Context) SystemDebugExportHostConditions {
	result := SystemDebugExportHostConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	uptime, err := host.UptimeWithContext(probeCtx)
	if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
		return result
	}
	result.UptimeSeconds = &uptime
	return result
}

func (s *SystemDebugExportService) collectProcessConditions(ctx context.Context) SystemDebugExportProcessConditions {
	result := SystemDebugExportProcessConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	proc, err := process.NewProcessWithContext(probeCtx, int32(os.Getpid()))
	if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
		return result
	}
	if info, err := proc.MemoryInfoWithContext(probeCtx); err == nil && info != nil {
		result.RSSBytes = &info.RSS
		result.VMSBytes = &info.VMS
	} else if err != nil {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	if percent, err := proc.PercentWithContext(probeCtx, 0); err == nil {
		result.CPUPercent = &percent
	} else {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	if threads, err := proc.NumThreadsWithContext(probeCtx); err == nil {
		result.NumThreads = &threads
	} else {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	if fds, err := proc.NumFDsWithContext(probeCtx); err == nil {
		result.NumFDs = &fds
	} else {
		markSystemDebugSectionPartial(&result.SystemDebugExportSectionStatus, err)
	}
	return result
}

func (s *SystemDebugExportService) collectDatabaseConditions(ctx context.Context) SystemDebugExportDatabaseConditions {
	result := SystemDebugExportDatabaseConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	if s.db == nil {
		result.Status = "unavailable"
		result.ErrorKind = "unavailable"
		return result
	}
	stats := s.db.Stats()
	result.Stats = SystemDebugExportDatabaseConnectionStats{
		OpenConnections: stats.OpenConnections,
		InUse:           stats.InUse,
		Idle:            stats.Idle,
		WaitCount:       stats.WaitCount,
		WaitDurationMs:  stats.WaitDuration.Milliseconds(),
		MaxOpen:         stats.MaxOpenConnections,
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	start := time.Now()
	if err := s.db.PingContext(probeCtx); err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	latency := time.Since(start).Milliseconds()
	result.LatencyMs = &latency
	return result
}

func (s *SystemDebugExportService) collectRedisConditions(ctx context.Context) SystemDebugExportRedisConditions {
	result := SystemDebugExportRedisConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	if s.redisClient == nil {
		result.Status = "unavailable"
		result.ErrorKind = "unavailable"
		return result
	}
	if stats := s.redisClient.PoolStats(); stats != nil {
		result.Pool = SystemDebugExportRedisPoolStats{
			Total:    int64(stats.TotalConns),
			Idle:     int64(stats.IdleConns),
			Stale:    int64(stats.StaleConns),
			Hits:     int64(stats.Hits),
			Misses:   int64(stats.Misses),
			Timeouts: int64(stats.Timeouts),
		}
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	start := time.Now()
	if err := s.redisClient.Ping(probeCtx).Err(); err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	latency := time.Since(start).Milliseconds()
	result.LatencyMs = &latency
	return result
}

func (s *SystemDebugExportService) collectLatestOpsMetrics(ctx context.Context) SystemDebugExportLatestOpsMetricsConditions {
	result := SystemDebugExportLatestOpsMetricsConditions{SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"}}
	if s.opsRepo == nil {
		result.Status = "unavailable"
		result.ErrorKind = "unavailable"
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	snapshot, err := s.opsRepo.GetLatestSystemMetrics(probeCtx, 1)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	if snapshot == nil {
		result.Status = "unavailable"
		result.ErrorKind = "not_found"
		return result
	}
	result.Snapshot = &SystemDebugExportLatestOpsMetricsSnapshot{
		CreatedAt:             snapshot.CreatedAt,
		WindowMinutes:         snapshot.WindowMinutes,
		CPUUsagePercent:       snapshot.CPUUsagePercent,
		MemoryUsedMB:          snapshot.MemoryUsedMB,
		MemoryTotalMB:         snapshot.MemoryTotalMB,
		MemoryUsagePercent:    snapshot.MemoryUsagePercent,
		DBOK:                  snapshot.DBOK,
		RedisOK:               snapshot.RedisOK,
		DBMaxOpenConns:        snapshot.DBMaxOpenConns,
		RedisPoolSize:         snapshot.RedisPoolSize,
		RedisConnTotal:        snapshot.RedisConnTotal,
		RedisConnIdle:         snapshot.RedisConnIdle,
		DBConnActive:          snapshot.DBConnActive,
		DBConnIdle:            snapshot.DBConnIdle,
		DBConnWaiting:         snapshot.DBConnWaiting,
		GoroutineCount:        snapshot.GoroutineCount,
		ConcurrencyQueueDepth: snapshot.ConcurrencyQueueDepth,
		AccountSwitchCount:    snapshot.AccountSwitchCount,
	}
	return result
}

func (s *SystemDebugExportService) collectOpsJobHeartbeats(ctx context.Context) SystemDebugExportOpsJobHeartbeatConditions {
	result := SystemDebugExportOpsJobHeartbeatConditions{
		SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"},
		Limit:                          systemDebugExportJobHeartbeatLimit,
		Heartbeats:                     []SystemDebugExportOpsJobHeartbeatSample{},
	}
	if s.opsRepo == nil {
		result.Status = "unavailable"
		result.ErrorKind = "unavailable"
		return result
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	heartbeats, err := s.opsRepo.ListJobHeartbeats(probeCtx, systemDebugExportJobHeartbeatLimit+1)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	sort.SliceStable(heartbeats, func(i, j int) bool {
		if heartbeats[i] == nil || heartbeats[j] == nil {
			return heartbeats[j] != nil
		}
		return heartbeats[i].JobName < heartbeats[j].JobName
	})
	for _, heartbeat := range heartbeats {
		if heartbeat == nil {
			continue
		}
		if result.Count >= systemDebugExportJobHeartbeatLimit {
			result.Truncated = true
			break
		}
		sample := SystemDebugExportOpsJobHeartbeatSample{
			JobName:        heartbeat.JobName,
			LastRunAt:      heartbeat.LastRunAt,
			LastSuccessAt:  heartbeat.LastSuccessAt,
			LastErrorAt:    heartbeat.LastErrorAt,
			LastDurationMs: heartbeat.LastDurationMs,
			UpdatedAt:      heartbeat.UpdatedAt,
		}
		if heartbeat.LastError != nil {
			sample.LastError = sanitizeSystemDebugExportText(*heartbeat.LastError)
		}
		result.Heartbeats = append(result.Heartbeats, sample)
		result.Count++
	}
	return result
}

func (s *SystemDebugExportService) collectAccountScheduling(ctx context.Context, now time.Time) SystemDebugExportAccountScheduling {
	result := SystemDebugExportAccountScheduling{
		SampleLimit:   systemDebugExportAccountSchedulingSampleLimit,
		BlockerCounts: map[string]int{},
		Samples:       []SystemDebugExportAccountSchedulingSample{},
	}
	if s.entClient == nil {
		result.CollectionError = sanitizeSystemDebugExportText("account scheduling collection unavailable: ent client is nil")
		return result
	}

	queryCtx, cancel := context.WithTimeout(ctx, systemDebugExportAccountSchedulingTimeout)
	defer cancel()

	matchingPredicate := systemDebugAccountSchedulingPredicate(now)
	matchingCount, err := s.entClient.Account.Query().
		Where(matchingPredicate).
		Count(queryCtx)
	if err != nil {
		result.CollectionError = systemDebugAccountSchedulingCollectionError(err)
		return result
	}
	result.MatchingCount = matchingCount
	result.Truncated = matchingCount > systemDebugExportAccountSchedulingSampleLimit

	summary, err := s.collectAccountSchedulingSummary(queryCtx, matchingPredicate)
	if err != nil {
		result.CollectionError = systemDebugAccountSchedulingCollectionError(err)
		return result
	}
	result.Summary = summary

	blockerCounts, err := s.collectAccountSchedulingBlockerCounts(queryCtx, now)
	if err != nil {
		result.CollectionError = systemDebugAccountSchedulingCollectionError(err)
		return result
	}
	result.BlockerCounts = blockerCounts

	accounts, err := s.entClient.Account.Query().
		Where(matchingPredicate).
		Select(
			account.FieldID,
			account.FieldPlatform,
			account.FieldType,
			account.FieldStatus,
			account.FieldAutoPauseOnExpired,
			account.FieldSchedulable,
			account.FieldLastUsedAt,
			account.FieldExpiresAt,
			account.FieldRateLimitedAt,
			account.FieldRateLimitResetAt,
			account.FieldOverloadUntil,
			account.FieldTempUnschedulableUntil,
			account.FieldTempUnschedulableReason,
			account.FieldSessionWindowStart,
			account.FieldSessionWindowEnd,
			account.FieldSessionWindowStatus,
			account.FieldErrorMessage,
		).
		Order(ent.Asc(account.FieldID)).
		Limit(systemDebugExportAccountSchedulingSampleLimit + 1).
		All(queryCtx)
	if err != nil {
		result.CollectionError = systemDebugAccountSchedulingCollectionError(err)
		return result
	}

	sampleAccounts := accounts
	if len(sampleAccounts) > systemDebugExportAccountSchedulingSampleLimit {
		result.Truncated = true
		sampleAccounts = sampleAccounts[:systemDebugExportAccountSchedulingSampleLimit]
	}

	for _, acct := range sampleAccounts {
		blockers := deriveSystemDebugAccountSchedulingBlockers(acct, now)

		sample := SystemDebugExportAccountSchedulingSample{
			AccountID:              acct.ID,
			Platform:               acct.Platform,
			Type:                   acct.Type,
			Status:                 acct.Status,
			Schedulable:            acct.Schedulable,
			LastUsedAt:             acct.LastUsedAt,
			ExpiresAt:              acct.ExpiresAt,
			RateLimitedAt:          acct.RateLimitedAt,
			RateLimitResetAt:       acct.RateLimitResetAt,
			OverloadUntil:          acct.OverloadUntil,
			TempUnschedulableUntil: acct.TempUnschedulableUntil,
			SessionWindowStart:     acct.SessionWindowStart,
			SessionWindowEnd:       acct.SessionWindowEnd,
			Blockers:               blockers,
		}
		if acct.SessionWindowStatus != nil {
			sample.SessionWindowStatus = *acct.SessionWindowStatus
		}
		if acct.ErrorMessage != nil {
			sample.ErrorMessage = sanitizeSystemDebugExportText(*acct.ErrorMessage)
		}
		if acct.TempUnschedulableReason != nil {
			sample.TempUnschedulableReason = sanitizeSystemDebugExportText(*acct.TempUnschedulableReason)
		}
		result.Samples = append(result.Samples, sample)
	}
	result.SampleCount = len(result.Samples)
	return result
}

func systemDebugAccountSchedulingPredicate(now time.Time) predicate.Account {
	return account.Or(
		account.StatusNEQ(StatusActive),
		account.SchedulableEQ(false),
		account.RateLimitResetAtGT(now),
		account.OverloadUntilGT(now),
		account.TempUnschedulableUntilGT(now),
		account.SessionWindowStatusEQ("rejected"),
		account.And(
			account.AutoPauseOnExpiredEQ(true),
			account.ExpiresAtLTE(now),
		),
	)
}

func (s *SystemDebugExportService) collectAccountSchedulingSummary(ctx context.Context, filter predicate.Account) (SystemDebugExportAccountSchedulingSummary, error) {
	byPlatform, err := s.collectAccountSchedulingGroupedCounts(ctx, filter, account.FieldPlatform)
	if err != nil {
		return SystemDebugExportAccountSchedulingSummary{}, err
	}
	byType, err := s.collectAccountSchedulingGroupedCounts(ctx, filter, account.FieldType)
	if err != nil {
		return SystemDebugExportAccountSchedulingSummary{}, err
	}
	byStatus, err := s.collectAccountSchedulingGroupedCounts(ctx, filter, account.FieldStatus)
	if err != nil {
		return SystemDebugExportAccountSchedulingSummary{}, err
	}
	return SystemDebugExportAccountSchedulingSummary{
		ByPlatform: byPlatform,
		ByType:     byType,
		ByStatus:   byStatus,
	}, nil
}
func (s *SystemDebugExportService) collectAccountSchedulingGroupedCounts(ctx context.Context, filter predicate.Account, field string) ([]SystemDebugExportAccountSchedulingCount, error) {
	switch field {
	case account.FieldPlatform:
		type platformRow struct {
			Platform string `json:"platform"`
			Count    int    `json:"count"`
		}
		var rows []platformRow
		err := s.entClient.Account.Query().
			Where(filter).
			GroupBy(account.FieldPlatform).
			Aggregate(ent.Count()).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}
		counts := make([]SystemDebugExportAccountSchedulingCount, 0, len(rows))
		for _, item := range rows {
			counts = append(counts, SystemDebugExportAccountSchedulingCount{Value: item.Platform, Count: item.Count})
		}
		return sortedSystemDebugAccountSchedulingCounts(counts), nil
	case account.FieldType:
		type typeRow struct {
			Type  string `json:"type"`
			Count int    `json:"count"`
		}
		var rows []typeRow
		err := s.entClient.Account.Query().
			Where(filter).
			GroupBy(account.FieldType).
			Aggregate(ent.Count()).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}
		counts := make([]SystemDebugExportAccountSchedulingCount, 0, len(rows))
		for _, item := range rows {
			counts = append(counts, SystemDebugExportAccountSchedulingCount{Value: item.Type, Count: item.Count})
		}
		return sortedSystemDebugAccountSchedulingCounts(counts), nil
	case account.FieldStatus:
		type statusRow struct {
			Status string `json:"status"`
			Count  int    `json:"count"`
		}
		var rows []statusRow
		err := s.entClient.Account.Query().
			Where(filter).
			GroupBy(account.FieldStatus).
			Aggregate(ent.Count()).
			Scan(ctx, &rows)
		if err != nil {
			return nil, err
		}
		counts := make([]SystemDebugExportAccountSchedulingCount, 0, len(rows))
		for _, item := range rows {
			counts = append(counts, SystemDebugExportAccountSchedulingCount{Value: item.Status, Count: item.Count})
		}
		return sortedSystemDebugAccountSchedulingCounts(counts), nil
	default:
		return nil, errors.New("unsupported account scheduling grouped count field")
	}
}

func (s *SystemDebugExportService) collectAccountSchedulingBlockerCounts(ctx context.Context, now time.Time) (map[string]int, error) {
	statusCounts, err := s.collectAccountSchedulingGroupedCounts(ctx, account.StatusNEQ(StatusActive), account.FieldStatus)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(statusCounts)+6)
	for _, statusCount := range statusCounts {
		if statusCount.Value == "" || statusCount.Value == StatusActive || statusCount.Count <= 0 {
			continue
		}
		counts["status_"+statusCount.Value] = statusCount.Count
	}

	blockers := []struct {
		name      string
		predicate predicate.Account
	}{
		{name: "schedulable_false", predicate: account.SchedulableEQ(false)},
		{name: "rate_limited", predicate: account.RateLimitResetAtGT(now)},
		{name: "overloaded", predicate: account.OverloadUntilGT(now)},
		{name: "temp_unschedulable", predicate: account.TempUnschedulableUntilGT(now)},
		{name: "session_rejected", predicate: account.SessionWindowStatusEQ("rejected")},
		{name: "expired_auto_paused", predicate: account.And(account.AutoPauseOnExpiredEQ(true), account.ExpiresAtLTE(now))},
	}
	for _, blocker := range blockers {
		count, err := s.entClient.Account.Query().
			Where(blocker.predicate).
			Count(ctx)
		if err != nil {
			return nil, err
		}
		if count > 0 {
			counts[blocker.name] = count
		}
	}
	return counts, nil
}

func sortedSystemDebugAccountSchedulingCounts(counts []SystemDebugExportAccountSchedulingCount) []SystemDebugExportAccountSchedulingCount {
	sort.Slice(counts, func(i, j int) bool {
		return counts[i].Value < counts[j].Value
	})
	return counts
}

func systemDebugAccountSchedulingCollectionError(err error) string {
	return "account scheduling collection failed: " + systemDebugErrorKind(err)
}

func (s *SystemDebugExportService) collectConfiguration() SystemDebugExportConfiguration {
	if s.cfg == nil {
		return SystemDebugExportConfiguration{}
	}
	cfg := s.cfg
	return SystemDebugExportConfiguration{
		Server: SystemDebugExportServerConfig{
			Mode:                   cfg.Server.Mode,
			ReadHeaderTimeout:      cfg.Server.ReadHeaderTimeout,
			IdleTimeout:            cfg.Server.IdleTimeout,
			TrustedProxyCount:      len(cfg.Server.TrustedProxies),
			MaxRequestBodySize:     cfg.Server.MaxRequestBodySize,
			H2CEnabled:             cfg.Server.H2C.Enabled,
			H2CMaxConcurrentStream: cfg.Server.H2C.MaxConcurrentStreams,
		},
		Database: SystemDebugExportDatabaseConfig{
			SSLMode:                cfg.Database.SSLMode,
			MaxOpenConns:           cfg.Database.MaxOpenConns,
			MaxIdleConns:           cfg.Database.MaxIdleConns,
			ConnMaxLifetimeMinutes: cfg.Database.ConnMaxLifetimeMinutes,
			ConnMaxIdleTimeMinutes: cfg.Database.ConnMaxIdleTimeMinutes,
		},
		Redis: SystemDebugExportRedisConfig{
			DB:                  cfg.Redis.DB,
			DialTimeoutSeconds:  cfg.Redis.DialTimeoutSeconds,
			ReadTimeoutSeconds:  cfg.Redis.ReadTimeoutSeconds,
			WriteTimeoutSeconds: cfg.Redis.WriteTimeoutSeconds,
			PoolSize:            cfg.Redis.PoolSize,
			MinIdleConns:        cfg.Redis.MinIdleConns,
			TLSEnabled:          cfg.Redis.EnableTLS,
		},
		Security: SystemDebugExportSecurityConfig{
			URLAllowlistEnabled:        cfg.Security.URLAllowlist.Enabled,
			UpstreamHostCount:          len(cfg.Security.URLAllowlist.UpstreamHosts),
			PricingHostCount:           len(cfg.Security.URLAllowlist.PricingHosts),
			CRSHostCount:               len(cfg.Security.URLAllowlist.CRSHosts),
			AllowPrivateHosts:          cfg.Security.URLAllowlist.AllowPrivateHosts,
			AllowInsecureHTTP:          cfg.Security.URLAllowlist.AllowInsecureHTTP,
			ResponseHeadersEnabled:     cfg.Security.ResponseHeaders.Enabled,
			AdditionalAllowedHeaders:   len(cfg.Security.ResponseHeaders.AdditionalAllowed),
			ForceRemovedHeaders:        len(cfg.Security.ResponseHeaders.ForceRemove),
			CSPEnabled:                 cfg.Security.CSP.Enabled,
			ProxyFallbackDirectOnError: cfg.Security.ProxyFallback.AllowDirectOnError,
		},
		Gateway: SystemDebugExportGatewayConfig{
			ResponseHeaderTimeoutSeconds:     cfg.Gateway.ResponseHeaderTimeout,
			MaxBodySize:                      cfg.Gateway.MaxBodySize,
			UpstreamResponseReadMaxBytes:     cfg.Gateway.UpstreamResponseReadMaxBytes,
			ProxyProbeResponseReadMaxBytes:   cfg.Gateway.ProxyProbeResponseReadMaxBytes,
			ConnectionPoolIsolation:          cfg.Gateway.ConnectionPoolIsolation,
			MaxIdleConns:                     cfg.Gateway.MaxIdleConns,
			MaxIdleConnsPerHost:              cfg.Gateway.MaxIdleConnsPerHost,
			MaxConnsPerHost:                  cfg.Gateway.MaxConnsPerHost,
			IdleConnTimeoutSeconds:           cfg.Gateway.IdleConnTimeoutSeconds,
			MaxUpstreamClients:               cfg.Gateway.MaxUpstreamClients,
			ClientIdleTTLSeconds:             cfg.Gateway.ClientIdleTTLSeconds,
			ConcurrencySlotTTLMinutes:        cfg.Gateway.ConcurrencySlotTTLMinutes,
			SessionIdleTimeoutMinutes:        cfg.Gateway.SessionIdleTimeoutMinutes,
			StreamDataIntervalTimeoutSeconds: cfg.Gateway.StreamDataIntervalTimeout,
			StreamKeepaliveIntervalSeconds:   cfg.Gateway.StreamKeepaliveInterval,
			ImageConcurrencyEnabled:          cfg.Gateway.ImageConcurrency.Enabled,
			ImageConcurrencyMaxConcurrent:    cfg.Gateway.ImageConcurrency.MaxConcurrentRequests,
			ImageConcurrencyOverflowMode:     cfg.Gateway.ImageConcurrency.OverflowMode,
			OpenAIWSEnabled:                  cfg.Gateway.OpenAIWS.Enabled,
			UserMessageQueueMode:             cfg.Gateway.UserMessageQueue.Mode,
		},
		Ops: SystemDebugExportOpsConfig{
			Enabled:                        cfg.Ops.Enabled,
			UsePreaggregatedTables:         cfg.Ops.UsePreaggregatedTables,
			CleanupEnabled:                 cfg.Ops.Cleanup.Enabled,
			CleanupSchedule:                cfg.Ops.Cleanup.Schedule,
			ErrorLogRetentionDays:          cfg.Ops.Cleanup.ErrorLogRetentionDays,
			MinuteMetricsRetentionDays:     cfg.Ops.Cleanup.MinuteMetricsRetentionDays,
			HourlyMetricsRetentionDays:     cfg.Ops.Cleanup.HourlyMetricsRetentionDays,
			MetricsCollectorCacheEnabled:   cfg.Ops.MetricsCollectorCache.Enabled,
			MetricsCollectorCacheTTLString: cfg.Ops.MetricsCollectorCache.TTL.String(),
			AggregationEnabled:             cfg.Ops.Aggregation.Enabled,
		},
		RateLimit: SystemDebugExportRateLimitConfig{
			OverloadCooldownMinutes: cfg.RateLimit.OverloadCooldownMinutes,
			OAuth401CooldownMinutes: cfg.RateLimit.OAuth401CooldownMinutes,
		},
		Concurrency: SystemDebugExportConcurrencyConfig{
			PingIntervalSeconds: cfg.Concurrency.PingInterval,
		},
	}
}

func (s *SystemDebugExportService) collectOps() SystemDebugExportOps {
	if s.opsCounters == nil {
		return SystemDebugExportOps{}
	}
	return SystemDebugExportOps{
		ErrorLogQueue: SystemDebugExportOpsErrorLogQueue{
			Length:         s.opsCounters.QueueLength(),
			Capacity:       s.opsCounters.QueueCapacity(),
			DroppedTotal:   s.opsCounters.DroppedTotal(),
			EnqueuedTotal:  s.opsCounters.EnqueuedTotal(),
			ProcessedTotal: s.opsCounters.ProcessedTotal(),
			SanitizedTotal: s.opsCounters.SanitizedTotal(),
		},
	}
}

func (s *SystemDebugExportService) collectLogAttribution(ctx context.Context, window SystemDebugExportLogAttributionWindow, limit int) SystemDebugExportLogAttribution {
	result := SystemDebugExportLogAttribution{
		SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"},
		Window:                         window,
		WindowHours:                    int(window.WindowSeconds / int64(time.Hour/time.Second)),
		Limit:                          limit,
		Capabilities: []string{
			"correlate_by_request_id_and_client_request_id",
			"narrow_to_component_endpoint_model_account_and_error_phase",
			"use_panic_or_error_stacktraces_when_present_in_standard_logs",
		},
		Limitations: []string{
			"raw_logs_request_bodies_headers_error_bodies_and_full_stacks_are_not_exported",
			"ops_log_indexes_do_not_guarantee_file_function_or_line_number_for_every_error",
			"account_scheduling_changes_do_not_persist_request_id_causality",
		},
	}
	if s.opsRepo == nil {
		result.Status = "unavailable"
		result.ErrorKind = "unavailable"
		result.SystemLogs = SystemDebugExportLogAttributionSystemLogs{
			SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "unavailable", ErrorKind: "unavailable"},
		}
		result.ErrorLogs = SystemDebugExportLogAttributionErrorLogs{
			SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "unavailable", ErrorKind: "unavailable"},
		}
		result.DiagnosticHints = append(result.DiagnosticHints, SystemDebugExportLogAttributionDiagnosticHint{
			Kind:    "ops_repository_unavailable",
			Summary: "Log attribution summaries could not be collected because the ops repository is unavailable.",
		})
		return result
	}

	start, err := time.Parse(time.RFC3339, window.Start)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = "invalid_window"
		return result
	}
	end, err := time.Parse(time.RFC3339, window.End)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = "invalid_window"
		return result
	}
	result.SystemLogs = s.collectLogAttributionSystemLogs(ctx, start, end, limit)
	result.ErrorLogs = s.collectLogAttributionErrorLogs(ctx, start, end, limit)
	if result.SystemLogs.Status == "partial" || result.SystemLogs.Status == "unavailable" || result.ErrorLogs.Status == "partial" || result.ErrorLogs.Status == "unavailable" {
		result.Status = "partial"
	}
	result.DiagnosticHints = buildSystemDebugLogAttributionHints(result.SystemLogs, result.ErrorLogs)
	return result
}

func (s *SystemDebugExportService) collectLogAttributionSystemLogs(ctx context.Context, start, end time.Time, limit int) SystemDebugExportLogAttributionSystemLogs {
	result := SystemDebugExportLogAttributionSystemLogs{
		SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"},
		Samples:                        []SystemDebugExportLogAttributionSystemSample{},
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	logs, truncated, err := s.opsRepo.SampleSystemLogsForDebugExport(probeCtx, start, end, limit)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	result.Truncated = truncated
	levelCounts := map[string]int{}
	componentCounts := map[string]int{}
	for _, item := range logs {
		if item == nil {
			continue
		}
		result.SampleCount++
		levelCounts[normalizeSystemDebugCountValue(item.Level, "unknown")]++
		componentCounts[normalizeSystemDebugCountValue(item.Component, "unknown")]++
		result.Samples = append(result.Samples, SystemDebugExportLogAttributionSystemSample{
			CreatedAt:       item.CreatedAt,
			Level:           sanitizeSystemDebugIdentifier(item.Level),
			Component:       sanitizeSystemDebugIdentifier(item.Component),
			RequestID:       sanitizeSystemDebugIdentifier(item.RequestID),
			ClientRequestID: sanitizeSystemDebugIdentifier(item.ClientRequestID),
			Platform:        sanitizeSystemDebugIdentifier(item.Platform),
			Model:           sanitizeSystemDebugIdentifier(item.Model),
			MessageExcerpt:  sanitizeSystemDebugExportText(item.Message),
		})
	}
	result.ByLevel = systemDebugCountsFromMap(levelCounts)
	result.ByComponent = systemDebugCountsFromMap(componentCounts)
	return result
}

func (s *SystemDebugExportService) collectLogAttributionErrorLogs(ctx context.Context, start, end time.Time, limit int) SystemDebugExportLogAttributionErrorLogs {
	result := SystemDebugExportLogAttributionErrorLogs{
		SystemDebugExportSectionStatus: SystemDebugExportSectionStatus{Status: "ok"},
		Samples:                        []SystemDebugExportLogAttributionErrorSample{},
	}
	probeCtx, cancel := context.WithTimeout(ctx, systemDebugExportProbeTimeout)
	defer cancel()
	errorsList, truncated, err := s.opsRepo.SampleErrorLogsForDebugExport(probeCtx, start, end, limit)
	if err != nil {
		result.Status = "unavailable"
		result.ErrorKind = systemDebugErrorKind(err)
		return result
	}
	result.Truncated = truncated
	phaseCounts := map[string]int{}
	typeCounts := map[string]int{}
	ownerCounts := map[string]int{}
	sourceCounts := map[string]int{}
	statusCounts := map[string]int{}
	for _, item := range errorsList {
		if item == nil {
			continue
		}
		result.SampleCount++
		phaseCounts[normalizeSystemDebugCountValue(item.Phase, "unknown")]++
		typeCounts[normalizeSystemDebugCountValue(item.Type, "unknown")]++
		ownerCounts[normalizeSystemDebugCountValue(item.Owner, "unknown")]++
		sourceCounts[normalizeSystemDebugCountValue(item.Source, "unknown")]++
		statusCounts[normalizeSystemDebugCountValue(statusCodeSystemDebugValue(item.StatusCode), "unknown")]++
		result.Samples = append(result.Samples, SystemDebugExportLogAttributionErrorSample{
			CreatedAt:        item.CreatedAt,
			RequestID:        sanitizeSystemDebugIdentifier(item.RequestID),
			ClientRequestID:  sanitizeSystemDebugIdentifier(item.ClientRequestID),
			Phase:            sanitizeSystemDebugIdentifier(item.Phase),
			Type:             sanitizeSystemDebugIdentifier(item.Type),
			Owner:            sanitizeSystemDebugIdentifier(item.Owner),
			Source:           sanitizeSystemDebugIdentifier(item.Source),
			Severity:         sanitizeSystemDebugIdentifier(item.Severity),
			StatusCode:       item.StatusCode,
			Platform:         sanitizeSystemDebugIdentifier(item.Platform),
			Model:            sanitizeSystemDebugIdentifier(item.Model),
			RequestPath:      sanitizeSystemDebugIdentifier(item.RequestPath),
			InboundEndpoint:  sanitizeSystemDebugIdentifier(item.InboundEndpoint),
			UpstreamEndpoint: sanitizeSystemDebugIdentifier(item.UpstreamEndpoint),
			MessageExcerpt:   sanitizeSystemDebugExportText(item.Message),
		})
	}
	result.ByPhase = systemDebugCountsFromMap(phaseCounts)
	result.ByType = systemDebugCountsFromMap(typeCounts)
	result.ByOwner = systemDebugCountsFromMap(ownerCounts)
	result.BySource = systemDebugCountsFromMap(sourceCounts)
	result.ByStatus = systemDebugCountsFromMap(statusCounts)
	return result
}

func deriveSystemDebugAccountSchedulingBlockers(acct *ent.Account, now time.Time) []string {
	if acct == nil {
		return nil
	}
	blockers := make([]string, 0, 7)
	if acct.Status != StatusActive {
		blockers = append(blockers, "status_"+acct.Status)
	}
	if !acct.Schedulable {
		blockers = append(blockers, "schedulable_false")
	}
	if acct.RateLimitResetAt != nil && acct.RateLimitResetAt.After(now) {
		blockers = append(blockers, "rate_limited")
	}
	if acct.OverloadUntil != nil && acct.OverloadUntil.After(now) {
		blockers = append(blockers, "overloaded")
	}
	if acct.TempUnschedulableUntil != nil && acct.TempUnschedulableUntil.After(now) {
		blockers = append(blockers, "temp_unschedulable")
	}
	if acct.SessionWindowStatus != nil && *acct.SessionWindowStatus == "rejected" {
		blockers = append(blockers, "session_rejected")
	}
	if acct.AutoPauseOnExpired && acct.ExpiresAt != nil && !acct.ExpiresAt.After(now) {
		blockers = append(blockers, "expired_auto_paused")
	}
	return blockers
}

func buildSystemDebugLogAttributionHints(systemLogs SystemDebugExportLogAttributionSystemLogs, errorLogs SystemDebugExportLogAttributionErrorLogs) []SystemDebugExportLogAttributionDiagnosticHint {
	hints := make([]SystemDebugExportLogAttributionDiagnosticHint, 0, 4)
	if len(errorLogs.ByPhase) > 0 {
		top := errorLogs.ByPhase[0]
		hints = append(hints, SystemDebugExportLogAttributionDiagnosticHint{
			Kind:    "dominant_error_phase",
			Summary: "Most sampled errors are in phase " + top.Value + ". Use this to narrow the investigation area before reading raw logs.",
		})
	}
	if len(systemLogs.ByComponent) > 0 {
		top := systemLogs.ByComponent[0]
		hints = append(hints, SystemDebugExportLogAttributionDiagnosticHint{
			Kind:    "dominant_log_component",
			Summary: "Most sampled indexed system logs come from component " + top.Value + ". This is a component-level hint, not a file/line proof.",
		})
	}
	if errorLogs.SampleCount == 0 && systemLogs.SampleCount == 0 {
		hints = append(hints, SystemDebugExportLogAttributionDiagnosticHint{
			Kind:    "no_recent_indexed_logs",
			Summary: "No indexed ops errors or system logs were found in the sampled window.",
		})
	}
	hints = append(hints, SystemDebugExportLogAttributionDiagnosticHint{
		Kind:    "code_location_limit",
		Summary: "This export intentionally provides request/component/error-phase attribution only; exact file/function/line requires raw server logs or a structured caller field.",
	})
	return hints
}

func systemDebugCountsFromMap(values map[string]int) []SystemDebugExportLogAttributionCount {
	counts := make([]SystemDebugExportLogAttributionCount, 0, len(values))
	for value, count := range values {
		if count <= 0 {
			continue
		}
		counts = append(counts, SystemDebugExportLogAttributionCount{Value: value, Count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Count == counts[j].Count {
			return counts[i].Value < counts[j].Value
		}
		return counts[i].Count > counts[j].Count
	})
	return counts
}

func normalizeSystemDebugCountValue(value, fallback string) string {
	value = sanitizeSystemDebugIdentifier(value)
	if value == "" {
		return fallback
	}
	return value
}

func statusCodeSystemDebugValue(statusCode int) string {
	if statusCode <= 0 {
		return ""
	}
	return strconv.Itoa(statusCode)
}

func sanitizeSystemDebugIdentifier(input string) string {
	return truncateSystemDebugExportText(sanitizeSystemDebugLogText(strings.TrimSpace(input)), 128)
}

func sanitizeSystemDebugExportText(input string) string {
	return truncateSystemDebugExportText(sanitizeSystemDebugLogText(input), 256)
}

func sanitizeSystemDebugLogText(input string) string {
	redacted := logredact.RedactText(input, systemDebugExportRedactKeys...)
	redacted = systemDebugLogBearerPattern.ReplaceAllString(redacted, "Bearer ***")
	redacted = systemDebugLogSKPattern.ReplaceAllString(redacted, "sk-***")
	redacted = systemDebugLogEmailPattern.ReplaceAllString(redacted, "***")
	redacted = systemDebugLogIPv4Pattern.ReplaceAllString(redacted, "***")
	return redacted
}

func NormalizeSystemDebugExportOptions(opts SystemDebugExportOptions) (SystemDebugExportOptions, error) {
	detailLevel := strings.ToLower(strings.TrimSpace(opts.DetailLevel))
	if detailLevel == "" {
		detailLevel = SystemDebugExportDetailStandard
	}
	switch detailLevel {
	case SystemDebugExportDetailStandard, SystemDebugExportDetailDetailed, SystemDebugExportDetailSupport:
	default:
		return SystemDebugExportOptions{}, fmt.Errorf("invalid debug export detail_level: %s", opts.DetailLevel)
	}

	sensitiveHandling := strings.ToLower(strings.TrimSpace(opts.SensitiveHandling))
	if sensitiveHandling == "" {
		sensitiveHandling = SystemDebugExportSensitiveMasked
	}
	switch sensitiveHandling {
	case SystemDebugExportSensitiveMasked, SystemDebugExportSensitiveDiagnostic:
	default:
		return SystemDebugExportOptions{}, fmt.Errorf("invalid debug export sensitive_handling: %s", opts.SensitiveHandling)
	}

	logWindowPreset := strings.ToLower(strings.TrimSpace(opts.LogWindowPreset))
	if logWindowPreset != "" {
		switch logWindowPreset {
		case SystemDebugExportLogWindowLast30Minutes,
			SystemDebugExportLogWindowLast6Hours,
			SystemDebugExportLogWindowLast1Day,
			SystemDebugExportLogWindowLast3Days,
			SystemDebugExportLogWindowLast1Week,
			SystemDebugExportLogWindowCustom:
		default:
			return SystemDebugExportOptions{}, fmt.Errorf("invalid debug export log_window_preset: %s", opts.LogWindowPreset)
		}
	}

	return SystemDebugExportOptions{
		DetailLevel:       detailLevel,
		SensitiveHandling: sensitiveHandling,
		LogWindowPreset:   logWindowPreset,
		CustomLogStart:    strings.TrimSpace(opts.CustomLogStart),
		CustomLogEnd:      strings.TrimSpace(opts.CustomLogEnd),
	}, nil
}

func buildSystemDebugExportManifest(opts SystemDebugExportOptions, logLimit int, logWindow SystemDebugExportLogAttributionWindow) SystemDebugExportManifest {
	return SystemDebugExportManifest{
		DetailLevel:       opts.DetailLevel,
		SensitiveHandling: opts.SensitiveHandling,
		GeneratedFor:      "admin_support_diagnostics",
		IncludedSections: []string{
			"system",
			"runtime",
			"server_conditions",
			"configuration",
			"ops",
			"log_attribution",
			"sensitive_diagnostics",
			"account_scheduling",
		},
		SafetyNotes: []string{
			"raw_secrets_are_never_exported",
			"more_sensitive_data_export_uses_presence_length_bucket_and_format_hints_only",
			"request_response_bodies_headers_raw_logs_credentials_and_payment_config_maps_are_excluded",
			"final_logredact_pass_is_always_applied",
		},
		Limits: SystemDebugExportManifestLimits{
			AccountSchedulingSamples:       systemDebugExportAccountSchedulingSampleLimit,
			LogAttributionSamples:          logLimit,
			JobHeartbeatSamples:            systemDebugExportJobHeartbeatLimit,
			LogAttributionWindowHours:      int(logWindow.WindowSeconds / int64(time.Hour/time.Second)),
			LogAttributionWindowMinutes:    logWindow.WindowMinutes,
			LogAttributionWindowSeconds:    logWindow.WindowSeconds,
			MaxLogAttributionWindowSeconds: logWindow.MaxWindowSeconds,
		},
		Timeouts: SystemDebugExportManifestTimeouts{
			ExportSeconds:            int(systemDebugExportTimeout / time.Second),
			ProbeMilliseconds:        int(systemDebugExportProbeTimeout / time.Millisecond),
			AccountSchedulingSeconds: int(systemDebugExportAccountSchedulingTimeout / time.Second),
		},
	}
}

func resolveSystemDebugLogAttributionWindow(opts SystemDebugExportOptions, now time.Time) (SystemDebugExportLogAttributionWindow, error) {
	end := now.UTC()
	preset := strings.TrimSpace(opts.LogWindowPreset)
	if preset == "" {
		preset = SystemDebugExportLogWindowDetailDefault
	}
	var start time.Time
	switch preset {
	case SystemDebugExportLogWindowLast30Minutes:
		start = end.Add(-30 * time.Minute)
	case SystemDebugExportLogWindowLast6Hours:
		start = end.Add(-6 * time.Hour)
	case SystemDebugExportLogWindowLast1Day:
		start = end.Add(-24 * time.Hour)
	case SystemDebugExportLogWindowLast3Days:
		start = end.Add(-72 * time.Hour)
	case SystemDebugExportLogWindowLast1Week:
		start = end.Add(-systemDebugExportMaxCustomLogAttributionWindow)
	case SystemDebugExportLogWindowCustom:
		customStart, err := parseSystemDebugCustomLogWindowTime(opts.CustomLogStart, "custom_log_start")
		if err != nil {
			return SystemDebugExportLogAttributionWindow{}, err
		}
		customEnd, err := parseSystemDebugCustomLogWindowTime(opts.CustomLogEnd, "custom_log_end")
		if err != nil {
			return SystemDebugExportLogAttributionWindow{}, err
		}
		if !customStart.Before(customEnd) {
			return SystemDebugExportLogAttributionWindow{}, fmt.Errorf("invalid debug export custom log window: custom_log_start must be before custom_log_end")
		}
		if customEnd.After(end) {
			return SystemDebugExportLogAttributionWindow{}, fmt.Errorf("invalid debug export custom log window: custom_log_end cannot be in the future")
		}
		if customEnd.Sub(customStart) > systemDebugExportMaxCustomLogAttributionWindow {
			return SystemDebugExportLogAttributionWindow{}, fmt.Errorf("invalid debug export custom log window: range cannot exceed 7 days")
		}
		start = customStart.UTC()
		end = customEnd.UTC()
	default:
		start = end.Add(-systemDebugExportLogAttributionWindowForDetail(opts.DetailLevel))
	}
	windowSeconds := int64(end.Sub(start) / time.Second)
	return SystemDebugExportLogAttributionWindow{
		Preset:           preset,
		Start:            start.UTC().Format(time.RFC3339),
		End:              end.UTC().Format(time.RFC3339),
		WindowSeconds:    windowSeconds,
		WindowMinutes:    int64(end.Sub(start) / time.Minute),
		MaxWindowSeconds: int64(systemDebugExportMaxCustomLogAttributionWindow / time.Second),
	}, nil
}

func parseSystemDebugCustomLogWindowTime(value, fieldName string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("invalid debug export custom log window: %s is required", fieldName)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid debug export custom log window: %s must be RFC3339", fieldName)
	}
	return parsed.UTC(), nil
}

func systemDebugExportLogAttributionLimitForDetail(detailLevel string) int {
	switch detailLevel {
	case SystemDebugExportDetailSupport:
		return 50
	case SystemDebugExportDetailDetailed:
		return 30
	default:
		return systemDebugExportLogAttributionLimit
	}
}

func systemDebugExportLogAttributionWindowForDetail(detailLevel string) time.Duration {
	switch detailLevel {
	case SystemDebugExportDetailSupport:
		return 72 * time.Hour
	case SystemDebugExportDetailDetailed:
		return 48 * time.Hour
	default:
		return systemDebugExportLogAttributionWindow
	}
}

func (s *SystemDebugExportService) collectSensitiveDiagnostics(opts SystemDebugExportOptions) SystemDebugExportSensitive {
	result := SystemDebugExportSensitive{
		Status:   "ok",
		Handling: opts.SensitiveHandling,
		Notices: []string{
			"Sensitive diagnostics never include plaintext passwords, API keys, tokens, cookies, private keys, request bodies, response bodies, or raw logs.",
			"Diagnostic mode adds configured flags, length buckets, and format hints without exporting reversible value fingerprints.",
		},
		Items: []SystemDebugExportSensitiveDiagnosticItem{},
	}
	if opts.SensitiveHandling != SystemDebugExportSensitiveDiagnostic {
		result.Notices = append(result.Notices, "Set sensitive_handling=diagnostic to include the additional non-plaintext sensitive diagnostics.")
		return result
	}
	if s.cfg == nil {
		result.Status = "unavailable"
		result.Notices = append(result.Notices, "Configuration is unavailable, so sensitive diagnostics could not be collected.")
		return result
	}

	appendSystemDebugSensitiveItem := func(name, category, value, hint string, notes ...string) {
		result.Items = append(result.Items, buildSystemDebugSensitiveDiagnosticItem(name, category, value, hint, notes...))
	}
	cfg := s.cfg
	appendSystemDebugSensitiveItem("database.host", "database_identifier", cfg.Database.Host, "hostname_or_ip")
	appendSystemDebugSensitiveItem("database.user", "database_identifier", cfg.Database.User, "identifier")
	appendSystemDebugSensitiveItem("database.name", "database_identifier", cfg.Database.DBName, "identifier")
	appendSystemDebugSensitiveItem("database.password", "database_credential", cfg.Database.Password, "credential")
	appendSystemDebugSensitiveItem("redis.host", "redis_identifier", cfg.Redis.Host, "hostname_or_ip")
	appendSystemDebugSensitiveItem("redis.password", "redis_credential", cfg.Redis.Password, "credential")
	appendSystemDebugSensitiveItem("jwt.signing_value", "auth_credential", cfg.JWT.Secret, "credential")
	appendSystemDebugSensitiveItem("totp.encryption_value", "auth_credential", cfg.Totp.EncryptionKey, "hex_or_base64_credential", fmt.Sprintf("manual_configured=%t", cfg.Totp.EncryptionKeyConfigured))
	appendSystemDebugSensitiveItem("default.admin_contact", "admin_identifier", cfg.Default.AdminEmail, "email_like")
	appendSystemDebugSensitiveItem("default.initial_password", "admin_credential", cfg.Default.AdminPassword, "credential")
	appendSystemDebugSensitiveItem("default.generated_key_prefix", "admin_identifier", cfg.Default.APIKeyPrefix, "prefix")
	appendSystemDebugSensitiveItem("gemini.oauth.client_id", "oauth_identifier", cfg.Gemini.OAuth.ClientID, "identifier")
	appendSystemDebugSensitiveItem("gemini.oauth.client_credential", "oauth_credential", cfg.Gemini.OAuth.ClientSecret, "credential")
	appendSystemDebugSensitiveItem("linuxdo.oauth.client_id", "oauth_identifier", cfg.LinuxDo.ClientID, "identifier", fmt.Sprintf("enabled=%t", cfg.LinuxDo.Enabled))
	appendSystemDebugSensitiveItem("linuxdo.oauth.client_credential", "oauth_credential", cfg.LinuxDo.ClientSecret, "credential", fmt.Sprintf("enabled=%t", cfg.LinuxDo.Enabled))
	appendSystemDebugSensitiveItem("wechat.oauth.app_id", "oauth_identifier", cfg.WeChat.AppID, "identifier", fmt.Sprintf("enabled=%t", cfg.WeChat.Enabled))
	appendSystemDebugSensitiveItem("wechat.oauth.app_credential", "oauth_credential", cfg.WeChat.AppSecret, "credential", fmt.Sprintf("enabled=%t", cfg.WeChat.Enabled))
	appendSystemDebugSensitiveItem("wechat.open.app_id", "oauth_identifier", cfg.WeChat.OpenAppID, "identifier", fmt.Sprintf("enabled=%t", cfg.WeChat.OpenEnabled))
	appendSystemDebugSensitiveItem("wechat.open.app_credential", "oauth_credential", cfg.WeChat.OpenAppSecret, "credential", fmt.Sprintf("enabled=%t", cfg.WeChat.OpenEnabled))
	appendSystemDebugSensitiveItem("wechat.mp.app_id", "oauth_identifier", cfg.WeChat.MPAppID, "identifier", fmt.Sprintf("enabled=%t", cfg.WeChat.MPEnabled))
	appendSystemDebugSensitiveItem("wechat.mp.app_credential", "oauth_credential", cfg.WeChat.MPAppSecret, "credential", fmt.Sprintf("enabled=%t", cfg.WeChat.MPEnabled))
	appendSystemDebugSensitiveItem("wechat.mobile.app_id", "oauth_identifier", cfg.WeChat.MobileAppID, "identifier", fmt.Sprintf("enabled=%t", cfg.WeChat.MobileEnabled))
	appendSystemDebugSensitiveItem("wechat.mobile.app_credential", "oauth_credential", cfg.WeChat.MobileAppSecret, "credential", fmt.Sprintf("enabled=%t", cfg.WeChat.MobileEnabled))
	appendSystemDebugSensitiveItem("oidc.client_id", "oauth_identifier", cfg.OIDC.ClientID, "identifier", fmt.Sprintf("enabled=%t", cfg.OIDC.Enabled))
	appendSystemDebugSensitiveItem("oidc.client_credential", "oauth_credential", cfg.OIDC.ClientSecret, "credential", fmt.Sprintf("enabled=%t", cfg.OIDC.Enabled))
	appendSystemDebugSensitiveItem("dingtalk.client_id", "oauth_identifier", cfg.DingTalk.ClientID, "identifier", fmt.Sprintf("enabled=%t", cfg.DingTalk.Enabled))
	appendSystemDebugSensitiveItem("dingtalk.client_credential", "oauth_credential", cfg.DingTalk.ClientSecret, "credential", fmt.Sprintf("enabled=%t", cfg.DingTalk.Enabled))
	appendSystemDebugSensitiveItem("github_oauth.client_id", "oauth_identifier", cfg.GitHubOAuth.ClientID, "identifier", fmt.Sprintf("enabled=%t", cfg.GitHubOAuth.Enabled))
	appendSystemDebugSensitiveItem("github_oauth.client_credential", "oauth_credential", cfg.GitHubOAuth.ClientSecret, "credential", fmt.Sprintf("enabled=%t", cfg.GitHubOAuth.Enabled))
	appendSystemDebugSensitiveItem("google_oauth.client_id", "oauth_identifier", cfg.GoogleOAuth.ClientID, "identifier", fmt.Sprintf("enabled=%t", cfg.GoogleOAuth.Enabled))
	appendSystemDebugSensitiveItem("google_oauth.client_credential", "oauth_credential", cfg.GoogleOAuth.ClientSecret, "credential", fmt.Sprintf("enabled=%t", cfg.GoogleOAuth.Enabled))
	return result
}

func buildSystemDebugSensitiveDiagnosticItem(name, category, value, hint string, notes ...string) SystemDebugExportSensitiveDiagnosticItem {
	trimmedValue := strings.TrimSpace(value)
	item := SystemDebugExportSensitiveDiagnosticItem{
		ItemName:      name,
		ValueCategory: category,
		Configured:    trimmedValue != "",
		FormatHint:    hint,
	}
	if len(notes) > 0 {
		item.Notes = append(item.Notes, notes...)
	}
	if trimmedValue == "" {
		item.LengthBucket = "empty"
		return item
	}
	item.LengthBucket = systemDebugSensitiveLengthBucket(trimmedValue)
	return item
}

func systemDebugSensitiveLengthBucket(value string) string {
	length := len([]rune(value))
	switch {
	case length == 0:
		return "empty"
	case length <= 8:
		return "1-8"
	case length <= 16:
		return "9-16"
	case length <= 32:
		return "17-32"
	case length <= 64:
		return "33-64"
	default:
		return "65+"
	}
}

func markSystemDebugSectionPartial(status *SystemDebugExportSectionStatus, err error) {
	if status == nil || err == nil {
		return
	}
	if status.Status == "" || status.Status == "ok" {
		status.Status = "partial"
	}
	if status.ErrorKind == "" {
		status.ErrorKind = systemDebugErrorKind(err)
	}
}

func systemDebugErrorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, redis.Nil) {
		return "not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "connection_failed"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded"):
		return "timeout"
	case strings.Contains(message, "canceled") || strings.Contains(message, "cancelled"):
		return "canceled"
	case strings.Contains(message, "permission denied") || strings.Contains(message, "access is denied"):
		return "permission_denied"
	case strings.Contains(message, "not found") || strings.Contains(message, "no such file") || strings.Contains(message, "no rows"):
		return "not_found"
	case strings.Contains(message, "connection refused") || strings.Contains(message, "connection reset") || strings.Contains(message, "no route") || strings.Contains(message, "network is unreachable") || strings.Contains(message, "dial"):
		return "connection_failed"
	case strings.Contains(message, "unavailable") || strings.Contains(message, "closed"):
		return "unavailable"
	default:
		return "unknown"
	}
}

func systemDebugExportRootPath() string {
	if runtime.GOOS != "windows" {
		return "/"
	}
	wd, err := os.Getwd()
	if err != nil {
		return `C:\`
	}
	volume := filepath.VolumeName(wd)
	if volume == "" {
		return `C:\`
	}
	return volume + `\`
}

func truncateSystemDebugExportText(input string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(input)
	if len(runes) <= limit {
		return input
	}
	return string(runes[:limit])
}

func systemDebugExportCountsFromMap(values map[string]int) []SystemDebugExportAccountSchedulingCount {
	counts := make([]SystemDebugExportAccountSchedulingCount, 0, len(values))
	for value, count := range values {
		counts = append(counts, SystemDebugExportAccountSchedulingCount{Value: value, Count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		return counts[i].Value < counts[j].Value
	})
	return counts
}

func safeRunMode(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return config.NormalizeRunMode(cfg.RunMode)
}

func safeTimezone(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.Timezone
}

func redactSystemDebugExportBundle(bundle SystemDebugExportBundle) (SystemDebugExportBundle, error) {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return SystemDebugExportBundle{}, err
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return SystemDebugExportBundle{}, err
	}
	redacted := logredact.RedactMap(generic, systemDebugExportRedactKeys...)
	redactedRaw, err := json.Marshal(redacted)
	if err != nil {
		return SystemDebugExportBundle{}, err
	}
	var output SystemDebugExportBundle
	if err := json.Unmarshal(redactedRaw, &output); err != nil {
		return SystemDebugExportBundle{}, err
	}
	return output, nil
}
