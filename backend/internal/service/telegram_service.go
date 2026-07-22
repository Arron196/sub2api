package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	telegramStartupTimeout = 35 * time.Second
)

type TelegramBotService struct {
	env                     config.TelegramConfig
	bindings                TelegramAdminBindingRepository
	state                   TelegramStateRepository
	botFactory              TelegramBotAPIFactory
	settingRepo             SettingRepository
	encryptor               SecretEncryptor
	encryptionKeyConfigured bool
	settings                *SettingService
	users                   TelegramAdminUserReader
	audit                   *AuditLogService
	admin                   AdminService
	groupRates              TelegramGroupRateMutationRepository
	authCacheInvalidator    APIKeyAuthCacheInvalidator

	lifecycleMu sync.Mutex
	runtime     atomic.Pointer[telegramRuntimeSnapshot]
	now         func() time.Time
}

func NewTelegramBotService(
	cfg *config.Config,
	bindings TelegramAdminBindingRepository,
	state TelegramStateRepository,
	bot TelegramBotAPI,
	settings *SettingService,
	users TelegramAdminUserReader,
	audit *AuditLogService,
	admin AdminService,
	groupRates TelegramGroupRateMutationRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *TelegramBotService {
	factory := func(string) TelegramBotAPI { return bot }
	var settingRepo SettingRepository
	if settings != nil {
		settingRepo = settings.settingRepo
	}
	return newTelegramBotService(cfg, bindings, state, factory, settingRepo, nil, settings, users, audit, admin, groupRates, authCacheInvalidator)
}

func NewManagedTelegramBotService(
	cfg *config.Config,
	bindings TelegramAdminBindingRepository,
	state TelegramStateRepository,
	botFactory TelegramBotAPIFactory,
	settingRepo SettingRepository,
	encryptor SecretEncryptor,
	settings *SettingService,
	users TelegramAdminUserReader,
	audit *AuditLogService,
	admin AdminService,
	groupRates TelegramGroupRateMutationRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *TelegramBotService {
	return newTelegramBotService(cfg, bindings, state, botFactory, settingRepo, encryptor, settings, users, audit, admin, groupRates, authCacheInvalidator)
}

func newTelegramBotService(
	cfg *config.Config,
	bindings TelegramAdminBindingRepository,
	state TelegramStateRepository,
	botFactory TelegramBotAPIFactory,
	settingRepo SettingRepository,
	encryptor SecretEncryptor,
	settings *SettingService,
	users TelegramAdminUserReader,
	audit *AuditLogService,
	admin AdminService,
	groupRates TelegramGroupRateMutationRepository,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
) *TelegramBotService {
	telegramConfig := config.TelegramConfig{}
	encryptionKeyConfigured := false
	if cfg != nil {
		telegramConfig = cfg.Telegram
		encryptionKeyConfigured = cfg.Totp.EncryptionKeyConfigured
	}
	telegramConfig.Normalize()
	svc := &TelegramBotService{
		env: telegramConfig, bindings: bindings, state: state, botFactory: botFactory,
		settingRepo: settingRepo, encryptor: encryptor, encryptionKeyConfigured: encryptionKeyConfigured, settings: settings,
		users: users, audit: audit, admin: admin, groupRates: groupRates,
		authCacheInvalidator: authCacheInvalidator, now: func() time.Time { return time.Now().UTC() },
	}
	svc.runtime.Store(svc.environmentRuntime("initializing"))
	return svc
}

func (s *TelegramBotService) AdminStatus(ctx context.Context, adminUserID int64) (*TelegramAdminStatus, error) {
	if s == nil || s.bindings == nil || s.state == nil {
		return nil, ErrTelegramUnavailable
	}
	if _, err := s.loadActiveAdmin(ctx, adminUserID); err != nil {
		return nil, err
	}
	if s.settingRepo != nil {
		_ = s.refreshRuntimeFromStore(ctx)
	}
	pending, err := s.state.GetVerificationCodeStatus(ctx, adminUserID)
	if err != nil {
		return nil, err
	}
	bindings, err := s.bindings.ListActiveBindings(ctx, adminUserID)
	if err != nil {
		return nil, err
	}
	var pendingExpiresAt *time.Time
	if pending != nil {
		expiresAt := pending.ExpiresAt
		pendingExpiresAt = &expiresAt
	}
	runtime := s.currentRuntime()
	status := &TelegramAdminStatus{PendingExpiresAt: pendingExpiresAt, Bindings: bindings}
	if runtime != nil {
		status.Configured = runtime.token != "" && runtime.botUsername != ""
		status.Enabled = runtime.enabled
		status.BotUsername = runtime.botUsername
		status.WebhookConfigured = runtime.webhookURL != "" && runtime.webhookSecret != ""
	}
	return status, nil
}

func (s *TelegramBotService) IssueVerificationCode(ctx context.Context, adminUserID int64) (*TelegramVerificationCode, error) {
	runtime := s.runtimeFromContext(ctx)
	if s == nil || !runtimeOperational(runtime) || s.state == nil || runtime.botUsername == "" {
		return nil, ErrTelegramUnavailable
	}
	if _, err := s.loadActiveAdmin(ctx, adminUserID); err != nil {
		return nil, err
	}
	return s.state.IssueVerificationCode(ctx, adminUserID)
}
func (s *TelegramBotService) CancelVerificationCode(ctx context.Context, adminUserID int64) error {
	if s == nil || !runtimeOperational(s.runtimeFromContext(ctx)) || s.state == nil {
		return ErrTelegramUnavailable
	}
	admin, err := s.loadActiveAdmin(ctx, adminUserID)
	if err != nil {
		return err
	}
	changed, err := s.state.CancelVerificationCode(ctx, adminUserID)
	if err != nil {
		return err
	}
	if changed {
		s.recordAudit(admin, "admin.telegram.verification.cancel", "web", nil)
	}
	return nil
}

func (s *TelegramBotService) RevokeBinding(ctx context.Context, adminUserID, bindingID int64) error {
	if s == nil || s.bindings == nil {
		return ErrTelegramUnavailable
	}
	admin, err := s.loadActiveAdmin(ctx, adminUserID)
	if err != nil {
		return err
	}
	binding, err := s.bindings.RevokeBinding(ctx, bindingID, adminUserID)
	if err != nil {
		return err
	}
	s.recordAudit(admin, "admin.telegram.binding.revoke", "web", map[string]any{
		"telegram_user_id": binding.TelegramUserID,
		"binding_id":       binding.ID,
	})
	return nil
}

func (s *TelegramBotService) ConsumeVerificationCode(ctx context.Context, code string, identity TelegramIdentity) (*TelegramBinding, error) {
	if s == nil || !runtimeOperational(s.runtimeFromContext(ctx)) || s.bindings == nil || s.state == nil {
		return nil, ErrTelegramUnavailable
	}
	code = normalizeTelegramVerificationCodeInput(code)
	if len(code) != TelegramVerificationCodeLength || identity.UserID <= 0 || identity.PrivateChatID != identity.UserID {
		return nil, ErrTelegramVerificationCodeInvalid
	}
	for _, ch := range code {
		if ch < '0' || ch > '9' && ch < 'A' || ch > 'Z' {
			return nil, ErrTelegramVerificationCodeInvalid
		}
	}
	adminUserID, remainingTTL, err := s.state.ConsumeVerificationCode(ctx, code)
	if err != nil {
		return nil, ErrTelegramVerificationCodeInvalid
	}
	admin, err := s.loadActiveAdmin(ctx, adminUserID)
	if err != nil {
		return nil, ErrTelegramVerificationCodeInvalid
	}
	binding, err := s.bindings.Bind(ctx, adminUserID, identity)
	if err != nil {
		_, restoreErr := s.state.RestoreVerificationCode(ctx, code, adminUserID, remainingTTL)
		return nil, errors.Join(err, restoreErr)
	}
	_ = s.state.ClearVerificationAttempts(ctx, identity.UserID)
	binding.AdminEmail = admin.Email
	s.recordAudit(admin, "admin.telegram.binding.create", "telegram", map[string]any{
		"binding_id":       binding.ID,
		"telegram_user_id": binding.TelegramUserID,
	})
	return binding, nil
}

func normalizeTelegramVerificationCodeInput(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	return code
}

func (s *TelegramBotService) loadActiveAdmin(ctx context.Context, adminUserID int64) (*User, error) {
	if s.users == nil {
		return nil, ErrTelegramUnavailable
	}
	user, err := s.users.GetByID(ctx, adminUserID)
	if err != nil || user == nil || !user.IsActive() || !user.IsAdmin() {
		return nil, ErrTelegramBindingNotFound
	}
	return user, nil
}

func (s *TelegramBotService) loadAuthorizedBinding(ctx context.Context, identity TelegramIdentity) (*TelegramBinding, *User, error) {
	if s.bindings == nil || identity.UserID <= 0 || identity.PrivateChatID != identity.UserID {
		return nil, nil, ErrTelegramBindingNotFound
	}
	binding, err := s.bindings.GetActiveBindingByTelegramUserID(ctx, identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	if binding.PrivateChatID != identity.PrivateChatID {
		return nil, nil, ErrTelegramBindingNotFound
	}
	user, err := s.loadActiveAdmin(ctx, binding.AdminUserID)
	if err != nil {
		return nil, nil, err
	}
	binding.AdminEmail = user.Email
	return binding, user, nil
}

func (s *TelegramBotService) recordAudit(user *User, action, authMethod string, extra map[string]any) {
	if s.audit == nil || user == nil {
		return
	}
	actorID := user.ID
	s.audit.Record(&AuditLog{
		ActorUserID: &actorID,
		ActorEmail:  user.Email,
		ActorRole:   user.Role,
		AuthMethod:  authMethod,
		Action:      action,
		Method:      "TELEGRAM",
		Path:        "telegram://admin",
		StatusCode:  200,
		Extra:       extra,
	})
}

func telegramDisplayName(user TelegramUser) string {
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	if name == "" {
		name = strings.TrimSpace(user.Username)
	}
	return truncateTelegramText(name, 160)
}

func truncateTelegramText(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

func (s *TelegramBotService) ProcessUpdate(ctx context.Context, update *TelegramUpdate) error {
	return s.processUpdateWithRuntime(ctx, update, s.currentRuntime())
}

func (s *TelegramBotService) processUpdateWithRuntime(
	ctx context.Context,
	update *TelegramUpdate,
	runtime *telegramRuntimeSnapshot,
) error {
	if s == nil || update == nil || s.state == nil || !runtimeOperational(runtime) {
		return ErrTelegramUnavailable
	}
	ctx = context.WithValue(ctx, telegramRuntimeContextKey{}, runtime)
	if update.CallbackQuery != nil {
		// Telegram expects callback queries to be acknowledged even for rejected actions.
		_ = runtime.client.AnswerCallbackQuery(ctx, update.CallbackQuery.ID, "")
	}
	claimed, err := s.state.ClaimUpdate(ctx, update.UpdateID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	var processErr error
	if update.CallbackQuery != nil {
		processErr = s.processCallback(ctx, update.CallbackQuery)
	} else if update.Message != nil {
		processErr = s.processMessage(ctx, update.Message)
	}
	if processErr != nil {
		return errors.Join(processErr, s.state.ReleaseUpdate(ctx, update.UpdateID))
	}
	return s.state.CompleteUpdate(ctx, update.UpdateID)
}

func telegramIdentityFromMessage(message *TelegramMessage) (TelegramIdentity, bool) {
	if message == nil || message.From == nil || message.From.IsBot || message.Chat.Type != "private" {
		return TelegramIdentity{}, false
	}
	if message.From.ID <= 0 || message.Chat.ID != message.From.ID {
		return TelegramIdentity{}, false
	}
	return TelegramIdentity{
		UserID: message.From.ID, PrivateChatID: message.Chat.ID,
		Username:    truncateTelegramText(message.From.Username, 64),
		DisplayName: telegramDisplayName(*message.From),
	}, true
}

func telegramIdentityFromCallback(callback *TelegramCallbackQuery) (TelegramIdentity, bool) {
	if callback == nil || callback.Message == nil || callback.From.IsBot || callback.Message.Chat.Type != "private" {
		return TelegramIdentity{}, false
	}
	if callback.From.ID <= 0 || callback.Message.Chat.ID != callback.From.ID {
		return TelegramIdentity{}, false
	}
	return TelegramIdentity{
		UserID: callback.From.ID, PrivateChatID: callback.Message.Chat.ID,
		Username:    truncateTelegramText(callback.From.Username, 64),
		DisplayName: telegramDisplayName(callback.From),
	}, true
}
