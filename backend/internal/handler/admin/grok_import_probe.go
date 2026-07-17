package admin

import (
	"context"
	"log/slog"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	grokImportProbeConcurrency = 3
	// QueryQuota may run the 20s billing query and then fall back to an active
	// 20s Responses probe, so leave room for both operations.
	grokImportProbeTimeout = 50 * time.Second
)

type grokQuotaRefresher interface {
	QueryQuota(ctx context.Context, accountID int64) (*service.GrokQuotaProbeResult, error)
}

type grokImportProbeTask struct {
	refresher grokQuotaRefresher
	accountID int64
}

type grokImportProbeScheduler struct {
	mu          sync.Mutex
	queue       []grokImportProbeTask
	concurrency int
	workers     int
	maxWorkers  int
	timeout     time.Duration
}

var defaultGrokImportProbeScheduler = newGrokImportProbeScheduler(
	grokImportProbeConcurrency,
	grokImportProbeTimeout,
)

func newGrokImportProbeScheduler(concurrency int, timeout time.Duration) *grokImportProbeScheduler {
	if concurrency <= 0 {
		concurrency = 1
	}
	if timeout <= 0 {
		timeout = grokImportProbeTimeout
	}
	return &grokImportProbeScheduler{
		concurrency: concurrency,
		timeout:     timeout,
	}
}

func (s *grokImportProbeScheduler) schedule(refresher grokQuotaRefresher, account *service.Account) {
	if s == nil || refresher == nil || account == nil || account.ID <= 0 {
		return
	}
	if account.Platform != service.PlatformGrok || account.Type != service.AccountTypeOAuth {
		return
	}

	s.mu.Lock()
	s.queue = append(s.queue, grokImportProbeTask{refresher: refresher, accountID: account.ID})
	if s.workers < s.concurrency {
		s.workers++
		if s.workers > s.maxWorkers {
			s.maxWorkers = s.workers
		}
		go s.worker()
	}
	s.mu.Unlock()
}

func (s *grokImportProbeScheduler) worker() {
	for {
		task, ok := s.nextTask()
		if !ok {
			return
		}
		s.run(task.refresher, task.accountID)
	}
}

func (s *grokImportProbeScheduler) nextTask() (grokImportProbeTask, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		s.workers--
		return grokImportProbeTask{}, false
	}
	task := s.queue[0]
	s.queue[0] = grokImportProbeTask{}
	s.queue = s.queue[1:]
	if len(s.queue) == 0 {
		s.queue = nil
	}
	return task, true
}

func (s *grokImportProbeScheduler) run(refresher grokQuotaRefresher, accountID int64) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error(
				"grok_import_quota_refresh_panic",
				"account_id", accountID,
				"recovery_type", panicType(recovered),
			)
		}
	}()

	// Queue time is intentionally excluded: every imported account is refreshed,
	// while this timeout only bounds the actual upstream work.
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	result, err := refresher.QueryQuota(ctx, accountID)
	if err != nil {
		slog.Warn(
			"grok_import_quota_refresh_failed",
			"account_id", accountID,
			"status", infraerrors.Code(err),
			"reason", infraerrors.Reason(err),
		)
		return
	}
	if result == nil {
		slog.Warn(
			"grok_import_quota_refresh_failed",
			"account_id", accountID,
			"reason", "empty_result",
		)
		return
	}

	slog.Info(
		"grok_import_quota_refresh_completed",
		"account_id", accountID,
		"source", result.Source,
		"model", result.Model,
		"status", result.StatusCode,
		"billing_observed", result.Billing != nil,
	)
}

func panicType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case error:
		return "error"
	default:
		return "unknown"
	}
}

func (h *AccountHandler) scheduleGrokImportProbe(account *service.Account) {
	if h == nil {
		return
	}
	defaultGrokImportProbeScheduler.schedule(h.grokQuotaRefresh, account)
}

func (h *GrokOAuthHandler) scheduleGrokImportProbe(account *service.Account) {
	if h == nil {
		return
	}
	defaultGrokImportProbeScheduler.schedule(h.quotaRefresh, account)
}

// ProvideAccountHandler injects the Grok active prober for production while
// keeping NewAccountHandler convenient for focused unit tests.
func ProvideAccountHandler(
	adminService service.AdminService,
	oauthService *service.OAuthService,
	openaiOAuthService *service.OpenAIOAuthService,
	geminiOAuthService *service.GeminiOAuthService,
	antigravityOAuthService *service.AntigravityOAuthService,
	grokOAuthService service.GrokOAuthTokenService,
	rateLimitService *service.RateLimitService,
	accountUsageService *service.AccountUsageService,
	accountTestService *service.AccountTestService,
	concurrencyService *service.ConcurrencyService,
	crsSyncService *service.CRSSyncService,
	sessionLimitCache service.SessionLimitCache,
	rpmCache service.RPMCache,
	tokenCacheInvalidator service.TokenCacheInvalidator,
	grokQuotaService *service.GrokQuotaService,
) *AccountHandler {
	handler := NewAccountHandler(
		adminService,
		oauthService,
		openaiOAuthService,
		geminiOAuthService,
		antigravityOAuthService,
		grokOAuthService,
		rateLimitService,
		accountUsageService,
		accountTestService,
		concurrencyService,
		crsSyncService,
		sessionLimitCache,
		rpmCache,
		tokenCacheInvalidator,
	)
	handler.grokQuotaRefresh = grokQuotaService
	return handler
}
