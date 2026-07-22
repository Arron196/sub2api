package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyTelegramBotEnabled             = "telegram_bot_enabled"
	SettingKeyTelegramBotTokenEncrypted      = "telegram_bot_token_encrypted"
	SettingKeyTelegramBotUsername            = "telegram_bot_username"
	SettingKeyTelegramBotWebhookURL          = "telegram_bot_webhook_url"
	SettingKeyTelegramWebhookSecretEncrypted = "telegram_bot_webhook_secret_encrypted"

	telegramConfigSourceDatabase    = "database"
	telegramConfigSourceEnvironment = "environment"
	telegramConfigSourceNone        = "none"
)

var ErrTelegramConfigInvalid = infraerrors.BadRequest(
	"TELEGRAM_CONFIG_INVALID",
	"Telegram bot configuration is invalid",
)

var ErrTelegramEncryptionKeyRequired = infraerrors.BadRequest(
	"SECRET_ENCRYPTION_KEY_NOT_CONFIGURED",
	"TOTP_ENCRYPTION_KEY must be configured before saving Telegram credentials",
)

var ErrTelegramConfigBusy = infraerrors.New(
	409,
	"TELEGRAM_CONFIG_BUSY",
	"Telegram bot configuration is being updated",
)

type TelegramBotConfigInput struct {
	Enabled    bool   `json:"enabled"`
	BotToken   string `json:"bot_token,omitempty"`
	WebhookURL string `json:"webhook_url,omitempty"`
}

type TelegramBotConfigView struct {
	Enabled           bool   `json:"enabled"`
	ConfigSource      string `json:"config_source"`
	TokenConfigured   bool   `json:"token_configured"`
	BotUsername       string `json:"bot_username"`
	WebhookURL        string `json:"webhook_url"`
	WebhookConfigured bool   `json:"webhook_configured"`
	LifecycleStatus   string `json:"lifecycle_status"`
}

type telegramRuntimeSnapshot struct {
	enabled         bool
	configSource    string
	token           string
	botUsername     string
	webhookURL      string
	webhookSecret   string
	client          TelegramBotAPI
	lifecycleStatus string
}
type telegramManagedConfig struct {
	enabledSet              bool
	enabled                 bool
	tokenCiphertext         string
	token                   string
	botUsername             string
	webhookURL              string
	webhookSecretCiphertext string
	webhookSecret           string
}

type TelegramWebhookSession struct {
	runtime *telegramRuntimeSnapshot
}

type telegramRuntimeContextKey struct{}

func (s *TelegramBotService) environmentRuntime(status string) *telegramRuntimeSnapshot {
	if s == nil {
		return &telegramRuntimeSnapshot{configSource: telegramConfigSourceNone, lifecycleStatus: "disabled"}
	}
	runtime := &telegramRuntimeSnapshot{
		enabled: s.env.Enabled, token: s.env.BotToken, botUsername: s.env.BotUsername,
		webhookURL: s.env.WebhookURL, webhookSecret: s.env.WebhookSecret,
		configSource: telegramConfigSourceNone, lifecycleStatus: status,
	}
	if runtime.token != "" {
		runtime.configSource = telegramConfigSourceEnvironment
		runtime.client = s.newBotClient(runtime.token)
	}
	if !runtime.enabled {
		runtime.lifecycleStatus = "disabled"
	}
	return runtime
}
func (s *TelegramBotService) newBotClient(token string) TelegramBotAPI {
	if s == nil || s.botFactory == nil || token == "" {
		return nil
	}
	return s.botFactory(token)
}

func (s *TelegramBotService) currentRuntime() *telegramRuntimeSnapshot {
	if s == nil {
		return nil
	}
	return s.runtime.Load()
}

func (s *TelegramBotService) runtimeFromContext(ctx context.Context) *telegramRuntimeSnapshot {
	if ctx != nil {
		if runtime, ok := ctx.Value(telegramRuntimeContextKey{}).(*telegramRuntimeSnapshot); ok {
			return runtime
		}
	}
	return s.currentRuntime()
}

func (s *TelegramBotService) botFromContext(ctx context.Context) TelegramBotAPI {
	runtime := s.runtimeFromContext(ctx)
	if runtime == nil {
		return nil
	}
	return runtime.client
}

func (s *TelegramBotService) botUsernameFromContext(ctx context.Context) string {
	runtime := s.runtimeFromContext(ctx)
	if runtime == nil {
		return ""
	}
	return runtime.botUsername
}
func (s *TelegramBotService) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	candidate := s.environmentRuntime("initializing")
	managed, err := s.loadManagedConfig(ctx)
	if err != nil {
		return s.publishStartupFailure(candidate)
	}
	candidate = s.resolveRuntime(managed, "initializing")
	if !candidate.enabled {
		candidate.lifecycleStatus = "disabled"
		if candidate.client != nil {
			cleanupCtx, cancel := context.WithTimeout(ctx, telegramStartupTimeout)
			defer cancel()
			if err := candidate.client.DeleteWebhook(cleanupCtx, false); err != nil {
				candidate.lifecycleStatus = "degraded"
				s.runtime.Store(candidate)
				return errors.New("telegram webhook cleanup failed")
			}
		}
		s.runtime.Store(candidate)
		return nil
	}
	if err := s.ensureWebhookSecret(candidate, false); err != nil {
		return s.publishStartupFailure(candidate)
	}
	startupCtx, cancel := context.WithTimeout(ctx, telegramStartupTimeout)
	defer cancel()
	if err := s.provisionCandidate(startupCtx, candidate); err != nil {
		if candidate.client != nil {
			_ = candidate.client.DeleteWebhook(startupCtx, false)
		}
		return s.publishStartupFailure(candidate)
	}
	if managed != nil && candidate.configSource == telegramConfigSourceDatabase {
		if err := s.persistStartupMetadata(startupCtx, managed, candidate); err != nil {
			_ = candidate.client.DeleteWebhook(startupCtx, false)
			return s.publishStartupFailure(candidate)
		}
	}
	candidate.lifecycleStatus = "ready"
	s.runtime.Store(candidate)
	return nil
}
func (s *TelegramBotService) publishStartupFailure(candidate *telegramRuntimeSnapshot) error {
	failed := *candidate
	failed.enabled = false
	failed.client = nil
	failed.lifecycleStatus = "degraded"
	s.runtime.Store(&failed)
	return errors.New("telegram lifecycle initialization failed")
}

func (s *TelegramBotService) loadManagedConfig(ctx context.Context) (*telegramManagedConfig, error) {
	if s == nil || s.settingRepo == nil {
		return &telegramManagedConfig{}, nil
	}
	keys := []string{
		SettingKeyTelegramBotEnabled, SettingKeyTelegramBotTokenEncrypted,
		SettingKeyTelegramBotUsername, SettingKeyTelegramBotWebhookURL,
		SettingKeyTelegramWebhookSecretEncrypted,
	}
	values, err := s.settingRepo.GetMultiple(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("load Telegram managed configuration: %w", err)
	}
	managed := &telegramManagedConfig{
		tokenCiphertext:         strings.TrimSpace(values[SettingKeyTelegramBotTokenEncrypted]),
		botUsername:             strings.TrimPrefix(strings.TrimSpace(values[SettingKeyTelegramBotUsername]), "@"),
		webhookURL:              strings.TrimSpace(values[SettingKeyTelegramBotWebhookURL]),
		webhookSecretCiphertext: strings.TrimSpace(values[SettingKeyTelegramWebhookSecretEncrypted]),
	}
	if raw, ok := values[SettingKeyTelegramBotEnabled]; ok {
		managed.enabledSet = true
		parsed, parseErr := strconv.ParseBool(strings.TrimSpace(raw))
		if parseErr != nil {
			return nil, errors.New("invalid Telegram enabled override")
		}
		managed.enabled = parsed
	}
	return s.decryptManagedConfig(managed)
}
func (s *TelegramBotService) decryptManagedConfig(managed *telegramManagedConfig) (*telegramManagedConfig, error) {
	if managed == nil {
		return &telegramManagedConfig{}, nil
	}
	if managed.tokenCiphertext != "" {
		if !s.encryptionKeyConfigured {
			return nil, errors.New("stable Telegram secret encryption key unavailable")
		}
		if s.encryptor == nil {
			return nil, errors.New("Telegram secret decryptor unavailable")
		}
		token, err := s.encryptor.Decrypt(managed.tokenCiphertext)
		if err != nil {
			return nil, errors.New("decrypt Telegram bot token")
		}
		managed.token = strings.TrimSpace(token)
	}
	if managed.webhookSecretCiphertext != "" {
		if !s.encryptionKeyConfigured {
			return nil, errors.New("stable Telegram secret encryption key unavailable")
		}
		if s.encryptor == nil {
			return nil, errors.New("Telegram secret decryptor unavailable")
		}
		secret, err := s.encryptor.Decrypt(managed.webhookSecretCiphertext)
		if err != nil {
			return nil, errors.New("decrypt Telegram webhook secret")
		}
		managed.webhookSecret = secret
	}
	return managed, nil
}

func (s *TelegramBotService) resolveRuntime(managed *telegramManagedConfig, status string) *telegramRuntimeSnapshot {
	runtime := s.environmentRuntime(status)
	if managed == nil {
		return runtime
	}
	if managed.enabledSet {
		runtime.enabled = managed.enabled
	}
	if managed.tokenCiphertext != "" {
		runtime.token = managed.token
		runtime.configSource = telegramConfigSourceDatabase
	}
	if managed.botUsername != "" {
		runtime.botUsername = managed.botUsername
	}
	if managed.webhookURL != "" {
		runtime.webhookURL = managed.webhookURL
	}
	if managed.webhookSecretCiphertext != "" {
		runtime.webhookSecret = managed.webhookSecret
	}
	runtime.client = s.newBotClient(runtime.token)
	if runtime.token == "" {
		runtime.configSource = telegramConfigSourceNone
	}
	if !runtime.enabled {
		runtime.lifecycleStatus = "disabled"
	}
	return runtime
}

func (s *TelegramBotService) ensureWebhookSecret(candidate *telegramRuntimeSnapshot, forceNew bool) error {
	if candidate == nil {
		return ErrTelegramConfigInvalid
	}
	if !forceNew && validTelegramWebhookSecret(candidate.webhookSecret) {
		return nil
	}
	if !forceNew && candidate.configSource != telegramConfigSourceDatabase {
		return ErrTelegramConfigInvalid
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return errors.New("generate Telegram webhook secret")
	}
	candidate.webhookSecret = base64.RawURLEncoding.EncodeToString(secretBytes)
	return nil
}

func (s *TelegramBotService) persistStartupMetadata(ctx context.Context, managed *telegramManagedConfig, candidate *telegramRuntimeSnapshot) error {
	if s.settingRepo == nil || s.encryptor == nil {
		return errors.New("Telegram configuration persistence unavailable")
	}
	updates := map[string]string{
		SettingKeyTelegramBotUsername:   candidate.botUsername,
		SettingKeyTelegramBotWebhookURL: candidate.webhookURL,
	}
	if managed.webhookSecretCiphertext == "" {
		ciphertext, err := s.encryptor.Encrypt(candidate.webhookSecret)
		if err != nil {
			return errors.New("encrypt Telegram webhook secret")
		}
		updates[SettingKeyTelegramWebhookSecretEncrypted] = ciphertext
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		return fmt.Errorf("persist Telegram startup metadata: %w", err)
	}
	return nil
}

func telegramBotCommands() []TelegramBotCommand {
	return []TelegramBotCommand{
		{Command: "start", Description: "打开管理菜单"},
		{Command: "bind", Description: "输入网页生成的验证码"},
		{Command: "settings", Description: "查看站点设置"},
		{Command: "status", Description: "查看站点状态"},
		{Command: "help", Description: "查看帮助"},
		{Command: "cancel", Description: "取消当前操作"},
	}
}

func (s *TelegramBotService) provisionCandidate(ctx context.Context, candidate *telegramRuntimeSnapshot) error {
	if err := validateTelegramCandidate(candidate); err != nil {
		return err
	}
	identity, err := candidate.client.GetMe(ctx)
	if err != nil || identity == nil || !identity.IsBot || !validTelegramBotUsername(identity.Username) {
		return ErrTelegramConfigInvalid
	}
	candidate.botUsername = strings.TrimPrefix(strings.TrimSpace(identity.Username), "@")
	if err := candidate.client.SetMyCommands(ctx, telegramBotCommands()); err != nil {
		return ErrTelegramConfigInvalid
	}
	if err := candidate.client.SetChatMenuButton(ctx); err != nil {
		return ErrTelegramConfigInvalid
	}
	if err := candidate.client.SetWebhook(
		ctx, candidate.webhookURL, candidate.webhookSecret,
		[]string{"message", "callback_query"},
	); err != nil {
		return ErrTelegramConfigInvalid
	}
	return nil
}

func validateTelegramCandidate(candidate *telegramRuntimeSnapshot) error {
	if candidate == nil || !validTelegramBotToken(candidate.token) || candidate.client == nil {
		return ErrTelegramConfigInvalid
	}
	if !validTelegramWebhookSecret(candidate.webhookSecret) {
		return ErrTelegramConfigInvalid
	}
	if err := validateTelegramPublicWebhookURL(candidate.webhookURL); err != nil {
		return ErrTelegramConfigInvalid
	}
	return nil
}

func validTelegramBotToken(token string) bool {
	if token == "" || len(token) > 512 || strings.TrimSpace(token) != token {
		return false
	}
	for _, ch := range token {
		if ch <= 0x20 || ch == 0x7f {
			return false
		}
	}
	return true
}

func validTelegramWebhookSecret(secret string) bool {
	if len(secret) < 1 || len(secret) > 256 {
		return false
	}
	for _, ch := range secret {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}
func validTelegramBotUsername(username string) bool {
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if len(username) < 5 || len(username) > 32 {
		return false
	}
	for _, ch := range username {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func validateTelegramPublicWebhookURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return ErrTelegramConfigInvalid
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") ||
		strings.HasSuffix(hostname, ".local") {
		return ErrTelegramConfigInvalid
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() ||
			ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return ErrTelegramConfigInvalid
		}
	}
	if port := parsed.Port(); port != "" && port != "443" && port != "80" && port != "88" && port != "8443" {
		return ErrTelegramConfigInvalid
	}
	return nil
}
func (s *TelegramBotService) GetConfig(ctx context.Context, adminUserID int64) (*TelegramBotConfigView, error) {
	if _, err := s.loadActiveAdmin(ctx, adminUserID); err != nil {
		return nil, err
	}
	if s.settingRepo != nil {
		_ = s.refreshRuntimeFromStore(ctx)
	}
	return telegramConfigView(s.currentRuntime()), nil
}

func telegramConfigView(runtime *telegramRuntimeSnapshot) *TelegramBotConfigView {
	if runtime == nil {
		return &TelegramBotConfigView{
			ConfigSource: telegramConfigSourceNone, LifecycleStatus: "disabled",
		}
	}
	return &TelegramBotConfigView{
		Enabled: runtime.enabled, ConfigSource: runtime.configSource,
		TokenConfigured: runtime.token != "", BotUsername: runtime.botUsername,
		WebhookURL:        runtime.webhookURL,
		WebhookConfigured: runtime.webhookURL != "" && runtime.webhookSecret != "",
		LifecycleStatus:   runtime.lifecycleStatus,
	}
}

func (s *TelegramBotService) UpdateConfig(
	ctx context.Context,
	adminUserID int64,
	input TelegramBotConfigInput,
) (view *TelegramBotConfigView, err error) {
	admin, err := s.loadActiveAdmin(ctx, adminUserID)
	if err != nil {
		return nil, err
	}
	if s.state == nil {
		return nil, ErrTelegramUnavailable
	}
	lockOwner, err := newTelegramConfigLockOwner()
	if err != nil {
		return nil, errors.New("generate Telegram configuration lock owner")
	}
	acquired, err := s.state.AcquireConfigLock(ctx, lockOwner, TelegramConfigLockTTL)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrTelegramConfigBusy
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err = errors.Join(err, s.state.ReleaseConfigLock(releaseCtx, lockOwner))
	}()
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if !input.Enabled {
		return s.disableManaged(ctx, admin)
	}
	return s.enableManaged(ctx, admin, input)
}
func (s *TelegramBotService) disableManaged(ctx context.Context, admin *User) (*TelegramBotConfigView, error) {
	if s.settingRepo == nil {
		return nil, errors.New("Telegram configuration persistence unavailable")
	}
	if err := s.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeyTelegramBotEnabled: "false",
	}); err != nil {
		return nil, fmt.Errorf("disable Telegram bot: %w", err)
	}
	previous := s.currentRuntime()
	disabled := s.environmentRuntime("disabled")
	if previous != nil {
		copy := *previous
		disabled = &copy
	}
	disabled.enabled = false
	disabled.lifecycleStatus = "disabled"
	s.runtime.Store(disabled)
	if previous != nil && previous.client != nil {
		if err := previous.client.DeleteWebhook(ctx, false); err != nil {
			disabled.lifecycleStatus = "degraded"
			s.runtime.Store(disabled)
			return telegramConfigView(disabled), errors.New("Telegram webhook cleanup failed")
		}
	}
	s.recordAudit(admin, "admin.telegram.config.update", "web", map[string]any{
		"enabled": false,
	})
	return telegramConfigView(disabled), nil
}

func (s *TelegramBotService) enableManaged(
	ctx context.Context,
	admin *User,
	input TelegramBotConfigInput,
) (*TelegramBotConfigView, error) {
	if s.settingRepo == nil || s.encryptor == nil {
		return nil, errors.New("Telegram configuration persistence unavailable")
	}
	if !s.encryptionKeyConfigured {
		return nil, ErrTelegramEncryptionKeyRequired
	}
	managed, err := s.loadManagedConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve Telegram bot configuration: %w", err)
	}
	candidate := s.resolveRuntime(managed, "provisioning")
	previous := s.currentRuntime()
	explicitToken := strings.TrimSpace(input.BotToken)
	if explicitToken != "" {
		candidate.token = explicitToken
		candidate.configSource = telegramConfigSourceDatabase
		candidate.client = s.newBotClient(explicitToken)
	}
	if webhookURL := strings.TrimSpace(input.WebhookURL); webhookURL != "" {
		candidate.webhookURL = webhookURL
	}
	candidate.enabled = true
	if err := s.ensureWebhookSecret(candidate, explicitToken != ""); err != nil {
		return nil, ErrTelegramConfigInvalid
	}
	secretCiphertext, err := s.encryptor.Encrypt(candidate.webhookSecret)
	if err != nil {
		return nil, errors.New("encrypt Telegram webhook secret")
	}
	tokenCiphertext := managed.tokenCiphertext
	if explicitToken != "" {
		tokenCiphertext, err = s.encryptor.Encrypt(candidate.token)
		if err != nil {
			return nil, errors.New("encrypt Telegram bot token")
		}
	}
	if candidate.configSource == telegramConfigSourceDatabase && tokenCiphertext == "" {
		return nil, ErrTelegramConfigInvalid
	}
	if err := s.provisionCandidate(ctx, candidate); err != nil {
		s.rollbackProvision(ctx, previous, candidate)
		return nil, ErrTelegramConfigInvalid
	}
	updates := map[string]string{
		SettingKeyTelegramBotEnabled:             "true",
		SettingKeyTelegramBotUsername:            candidate.botUsername,
		SettingKeyTelegramBotWebhookURL:          candidate.webhookURL,
		SettingKeyTelegramWebhookSecretEncrypted: secretCiphertext,
	}
	if candidate.configSource == telegramConfigSourceDatabase {
		updates[SettingKeyTelegramBotTokenEncrypted] = tokenCiphertext
	}
	if err := s.settingRepo.SetMultiple(ctx, updates); err != nil {
		s.rollbackProvision(ctx, previous, candidate)
		return nil, fmt.Errorf("persist Telegram bot configuration: %w", err)
	}
	candidate.lifecycleStatus = "ready"
	s.runtime.Store(candidate)
	if s.settings != nil && s.settings.onUpdate != nil {
		s.settings.onUpdate()
	}
	if previous != nil && previous.client != nil && previous.token != candidate.token {
		if err := previous.client.DeleteWebhook(ctx, false); err != nil {
			candidate.lifecycleStatus = "degraded"
			s.runtime.Store(candidate)
			return telegramConfigView(candidate), errors.New("previous Telegram webhook cleanup failed")
		}
	}
	s.recordAudit(admin, "admin.telegram.config.update", "web", map[string]any{
		"enabled": true, "config_source": candidate.configSource,
		"bot_username": candidate.botUsername, "webhook_url": candidate.webhookURL,
	})
	return telegramConfigView(candidate), nil
}

func (s *TelegramBotService) rollbackProvision(
	ctx context.Context,
	previous *telegramRuntimeSnapshot,
	candidate *telegramRuntimeSnapshot,
) {
	if candidate == nil || candidate.client == nil {
		return
	}
	if previous != nil && previous.enabled && previous.client != nil &&
		previous.token == candidate.token && validTelegramWebhookSecret(previous.webhookSecret) {
		_ = previous.client.SetWebhook(
			ctx, previous.webhookURL, previous.webhookSecret,
			[]string{"message", "callback_query"},
		)
		return
	}
	_ = candidate.client.DeleteWebhook(ctx, false)
}
func (s *TelegramBotService) BotUsername() string {
	runtime := s.currentRuntime()
	if runtime == nil {
		return ""
	}
	return runtime.botUsername
}

func (s *TelegramBotService) WebhookReady() (enabled, configured bool) {
	runtime := s.currentRuntime()
	if runtime == nil {
		return false, false
	}
	enabled = runtime.enabled
	configured = runtimeOperational(runtime) && runtime.webhookSecret != "" &&
		s.bindings != nil && s.state != nil
	return enabled, configured
}

func (s *TelegramBotService) AuthorizeWebhook(ctx context.Context, value string) (
	*TelegramWebhookSession,
	bool,
	bool,
) {
	if s != nil && s.settingRepo != nil {
		_ = s.refreshRuntimeFromStore(ctx)
	}
	runtime := s.currentRuntime()
	if runtime == nil || !runtime.enabled {
		return nil, false, false
	}
	configured := runtimeOperational(runtime) && runtime.webhookSecret != "" &&
		s.bindings != nil && s.state != nil
	if !configured || !constantTimeTelegramSecretEqual(runtime.webhookSecret, value) {
		return nil, true, configured
	}
	return &TelegramWebhookSession{runtime: runtime}, true, true
}

func (s *TelegramBotService) VerifyWebhookSecret(value string) bool {
	session, _, _ := s.AuthorizeWebhook(context.Background(), value)
	return session != nil
}

func (s *TelegramBotService) refreshRuntimeFromStore(ctx context.Context) error {
	if s == nil || s.settingRepo == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	managed, err := s.loadManagedConfig(ctx)
	if err != nil {
		failed := s.currentRuntime()
		if failed == nil {
			failed = s.environmentRuntime("degraded")
		} else {
			copy := *failed
			failed = &copy
		}
		failed.client = nil
		failed.lifecycleStatus = "degraded"
		s.runtime.Store(failed)
		return err
	}
	candidate := s.resolveRuntime(managed, "ready")
	if sameTelegramRuntimeConfiguration(s.currentRuntime(), candidate) {
		return nil
	}
	if !candidate.enabled {
		current := s.currentRuntime()
		if current != nil && current.client != nil && (current.enabled || current.lifecycleStatus == "degraded") {
			if err := current.client.DeleteWebhook(ctx, false); err != nil {
				candidate.client = nil
				candidate.lifecycleStatus = "degraded"
				s.runtime.Store(candidate)
				return err
			}
		}
		candidate.lifecycleStatus = "disabled"
		s.runtime.Store(candidate)
		return nil
	}
	if err := validateTelegramCandidate(candidate); err != nil {
		candidate.client = nil
		candidate.lifecycleStatus = "degraded"
		s.runtime.Store(candidate)
		return err
	}
	candidate.lifecycleStatus = "ready"
	s.runtime.Store(candidate)
	return nil
}

func sameTelegramRuntimeConfiguration(current, candidate *telegramRuntimeSnapshot) bool {
	if current == nil || candidate == nil {
		return current == candidate
	}
	if current.lifecycleStatus == "degraded" || candidate.enabled && current.client == nil {
		return false
	}
	return current.enabled == candidate.enabled &&
		current.configSource == candidate.configSource &&
		current.token == candidate.token &&
		current.webhookURL == candidate.webhookURL &&
		current.webhookSecret == candidate.webhookSecret
}

func newTelegramConfigLockOwner() (string, error) {
	owner := make([]byte, 18)
	if _, err := rand.Read(owner); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(owner), nil
}
func (s *TelegramBotService) ProcessWebhookUpdate(
	ctx context.Context,
	session *TelegramWebhookSession,
	update *TelegramUpdate,
) error {
	if session == nil || session.runtime == nil {
		return ErrTelegramUnavailable
	}
	return s.processUpdateWithRuntime(ctx, update, session.runtime)
}

func runtimeOperational(runtime *telegramRuntimeSnapshot) bool {
	if runtime == nil || !runtime.enabled || runtime.client == nil {
		return false
	}
	return runtime.lifecycleStatus == "ready" || runtime.lifecycleStatus == "initializing"
}

func constantTimeTelegramSecretEqual(configured, provided string) bool {
	if configured == "" || provided == "" {
		return false
	}
	configuredHash := sha256.Sum256([]byte(configured))
	providedHash := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(configuredHash[:], providedHash[:]) == 1
}

func isTelegramManagedSettingKey(key string) bool {
	switch key {
	case SettingKeyTelegramBotEnabled,
		SettingKeyTelegramBotTokenEncrypted,
		SettingKeyTelegramBotUsername,
		SettingKeyTelegramBotWebhookURL,
		SettingKeyTelegramWebhookSecretEncrypted:
		return true
	default:
		return false
	}
}
