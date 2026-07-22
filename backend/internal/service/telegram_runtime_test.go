package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestTelegramManagedEnableEncryptsAndPublishesWithoutSecretEcho(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	encryptor := telegramRuntimeEncryptor{}
	bot := newTelegramLifecycleBot("managed_bot")
	factory := telegramLifecycleFactory(map[string]*telegramLifecycleBot{
		"123456:managed-token": bot,
	})
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, encryptor, factory, nil)

	view, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:managed-token",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	require.True(t, view.Enabled)
	require.Equal(t, telegramConfigSourceDatabase, view.ConfigSource)
	require.True(t, view.TokenConfigured)
	require.Equal(t, "managed_bot", view.BotUsername)
	require.Equal(t, "ready", view.LifecycleStatus)
	require.Equal(t, 1, bot.getMeCalls)
	require.Equal(t, 1, bot.commandCalls)
	require.Equal(t, 1, bot.menuCalls)
	require.Equal(t, 1, bot.webhookCalls)
	require.False(t, bot.lastDropPending)
	require.NotEmpty(t, bot.webhookSecret)

	storedToken := repo.values[SettingKeyTelegramBotTokenEncrypted]
	storedSecret := repo.values[SettingKeyTelegramWebhookSecretEncrypted]
	require.NotEmpty(t, storedToken)
	require.NotEmpty(t, storedSecret)
	require.NotEqual(t, "123456:managed-token", storedToken)
	require.NotEqual(t, bot.webhookSecret, storedSecret)
	storedJSON, err := json.Marshal(repo.values)
	require.NoError(t, err)
	require.NotContains(t, string(storedJSON), "123456:managed-token")
	require.NotContains(t, string(storedJSON), bot.webhookSecret)

	responseJSON, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(responseJSON), "123456:managed-token")
	require.NotContains(t, string(responseJSON), bot.webhookSecret)
	require.NotContains(t, string(responseJSON), "bot_token")
	require.NotContains(t, string(responseJSON), "webhook_secret")
}

func TestTelegramManagedConfigRejectsEphemeralEncryptionKey(t *testing.T) {
	repo := newTelegramRuntimeSettingRepo()
	bot := newTelegramLifecycleBot("managed_bot")
	cfg := &config.Config{}
	settings := NewSettingService(repo, cfg)
	svc := NewManagedTelegramBotService(
		cfg,
		newTelegramBindingStub(),
		newTelegramStateStub(),
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:managed-token": bot}),
		repo,
		telegramRuntimeEncryptor{},
		settings,
		telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}},
		nil,
		nil,
		nil,
		nil,
	)

	view, err := svc.UpdateConfig(context.Background(), 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:managed-token",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.Nil(t, view)
	require.ErrorIs(t, err, ErrTelegramEncryptionKeyRequired)
	require.Empty(t, repo.values)
}

func TestTelegramManagedBlankTokenPreservesStoredToken(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	bot := newTelegramLifecycleBot("preserved_bot")
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:preserved": bot}), nil)

	_, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:preserved",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	storedToken := repo.values[SettingKeyTelegramBotTokenEncrypted]
	storedSecret := repo.values[SettingKeyTelegramWebhookSecretEncrypted]
	webhookSecret := bot.webhookSecret

	view, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, telegramConfigSourceDatabase, view.ConfigSource)
	require.Equal(t, storedToken, repo.values[SettingKeyTelegramBotTokenEncrypted])
	require.Equal(t, storedSecret, repo.values[SettingKeyTelegramWebhookSecretEncrypted])
	require.Equal(t, webhookSecret, bot.webhookSecret)
	require.Equal(t, 2, bot.getMeCalls)
}

func TestTelegramManagedInvalidGetMeLeavesOldRuntimeAndSettingsActive(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	oldBot := newTelegramLifecycleBot("old_bot")
	badBot := newTelegramLifecycleBot("bad_bot")
	badBot.getMeErr = errors.New("upstream rejected 654321:invalid-token")
	factory := telegramLifecycleFactory(map[string]*telegramLifecycleBot{
		"123456:old-token":     oldBot,
		"654321:invalid-token": badBot,
	})
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{}, factory, nil)
	_, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:old-token",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	oldRuntime := svc.currentRuntime()
	oldValues := cloneTelegramRuntimeValues(repo.values)

	view, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "654321:invalid-token",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.Nil(t, view)
	require.ErrorIs(t, err, ErrTelegramConfigInvalid)
	require.NotContains(t, err.Error(), "654321:invalid-token")
	require.NotContains(t, err.Error(), "upstream rejected")
	require.Same(t, oldRuntime, svc.currentRuntime())
	require.Equal(t, oldValues, repo.values)
	require.True(t, svc.VerifyWebhookSecret(oldBot.webhookSecret))
	require.Equal(t, 1, badBot.deleteCalls)
}

func TestTelegramManagedDisablePreservesBindingVisibilityAndDeletesWebhook(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	bot := newTelegramLifecycleBot("disable_bot")
	bindings := newTelegramBindingStub()
	binding := &TelegramBinding{ID: 19, AdminUserID: 7, TelegramUserID: 99, PrivateChatID: 99}
	bindings.byTelegram[99] = binding
	bindings.byID[19] = binding
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:disable": bot}), bindings)
	_, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:disable",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)

	view, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{Enabled: false})
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.Equal(t, "disabled", view.LifecycleStatus)
	require.Equal(t, "false", repo.values[SettingKeyTelegramBotEnabled])
	require.Equal(t, 1, bot.deleteCalls)
	require.False(t, bot.lastDropPending)

	status, err := svc.AdminStatus(ctx, 7)
	require.NoError(t, err)
	require.False(t, status.Enabled)
	require.Len(t, status.Bindings, 1)
	_, err = svc.IssueVerificationCode(ctx, 7)
	require.ErrorIs(t, err, ErrTelegramUnavailable)
}
func TestTelegramManagedRotationRejectsOldWebhookSecret(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	oldBot := newTelegramLifecycleBot("old_bot")
	newBot := newTelegramLifecycleBot("new_bot")
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{
			"123456:old": oldBot, "654321:new": newBot,
		}), nil)
	_, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:old",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	oldSecret := oldBot.webhookSecret
	require.True(t, svc.VerifyWebhookSecret(oldSecret))

	_, err = svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "654321:new",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	require.NotEqual(t, oldSecret, newBot.webhookSecret)
	require.False(t, svc.VerifyWebhookSecret(oldSecret))
	require.True(t, svc.VerifyWebhookSecret(newBot.webhookSecret))
	require.Equal(t, 1, oldBot.deleteCalls)
}

func TestTelegramManagedStartupUsesEnvironmentWhenOverrideAbsent(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	bot := newTelegramLifecycleBot("environment_bot")
	env := config.TelegramConfig{
		Enabled: true, BotToken: "123456:environment", BotUsername: "configured_name",
		WebhookURL:    "https://example.com/api/v1/telegram/webhook",
		WebhookSecret: "environment_secret",
	}
	svc := newManagedTelegramTestService(env, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:environment": bot}), nil)

	require.NoError(t, svc.Start(ctx))
	view, err := svc.GetConfig(ctx, 7)
	require.NoError(t, err)
	require.True(t, view.Enabled)
	require.Equal(t, telegramConfigSourceEnvironment, view.ConfigSource)
	require.Equal(t, "environment_bot", view.BotUsername)
	require.Equal(t, "ready", view.LifecycleStatus)
	require.Empty(t, repo.values)
	require.Equal(t, 1, bot.getMeCalls)
	require.Equal(t, 1, bot.webhookCalls)
}

func TestTelegramManagedStartupExplicitFalseOverridesEnvironment(t *testing.T) {
	repo := newTelegramRuntimeSettingRepo()
	repo.values[SettingKeyTelegramBotEnabled] = "false"
	bot := newTelegramLifecycleBot("environment_bot")
	env := config.TelegramConfig{
		Enabled: true, BotToken: "123456:environment",
		WebhookURL:    "https://example.com/api/v1/telegram/webhook",
		WebhookSecret: "environment_secret",
	}
	svc := newManagedTelegramTestService(env, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:environment": bot}), nil)

	require.NoError(t, svc.Start(context.Background()))
	view, err := svc.GetConfig(context.Background(), 7)
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.Equal(t, "disabled", view.LifecycleStatus)
	require.Zero(t, bot.getMeCalls)
}

func TestTelegramManagedStartupFailsClosedOnCorruptManagedSecret(t *testing.T) {
	repo := newTelegramRuntimeSettingRepo()
	repo.values[SettingKeyTelegramBotEnabled] = "true"
	repo.values[SettingKeyTelegramBotTokenEncrypted] = "not-valid-ciphertext"
	envBot := newTelegramLifecycleBot("environment_bot")
	env := config.TelegramConfig{
		Enabled: true, BotToken: "123456:environment",
		WebhookURL: "https://example.com/api/v1/telegram/webhook", WebhookSecret: "environment_secret",
	}
	svc := newManagedTelegramTestService(env, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:environment": envBot}), nil)

	require.Error(t, svc.Start(context.Background()))
	runtime := svc.currentRuntime()
	require.NotNil(t, runtime)
	require.Equal(t, "degraded", runtime.lifecycleStatus)
	require.False(t, svc.VerifyWebhookSecret("environment_secret"))
	require.Zero(t, envBot.webhookCalls)
}

func TestTelegramManagedRefreshRejectsOldSecretAcrossInstances(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	oldBot := newTelegramLifecycleBot("old_bot")
	newBot := newTelegramLifecycleBot("new_bot")
	factory := telegramLifecycleFactory(map[string]*telegramLifecycleBot{
		"123456:old": oldBot,
		"654321:new": newBot,
	})
	first := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{}, factory, nil)
	second := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{}, factory, nil)

	_, err := first.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:old",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	require.NoError(t, second.Start(ctx))
	oldSecret := oldBot.webhookSecret
	require.True(t, second.VerifyWebhookSecret(oldSecret))

	_, err = first.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "654321:new",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	require.False(t, second.VerifyWebhookSecret(oldSecret))
	require.True(t, second.VerifyWebhookSecret(newBot.webhookSecret))
}

func TestTelegramManagedDisableReportsWebhookCleanupFailure(t *testing.T) {
	ctx := context.Background()
	repo := newTelegramRuntimeSettingRepo()
	bot := newTelegramLifecycleBot("managed_bot")
	svc := newManagedTelegramTestService(config.TelegramConfig{}, repo, telegramRuntimeEncryptor{},
		telegramLifecycleFactory(map[string]*telegramLifecycleBot{"123456:managed": bot}), nil)
	_, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{
		Enabled: true, BotToken: "123456:managed",
		WebhookURL: "https://example.com/api/v1/telegram/webhook",
	})
	require.NoError(t, err)
	bot.deleteErr = errors.New("cleanup failed")

	view, err := svc.UpdateConfig(ctx, 7, TelegramBotConfigInput{Enabled: false})
	require.Error(t, err)
	require.NotNil(t, view)
	require.False(t, view.Enabled)
	require.Equal(t, "degraded", view.LifecycleStatus)
	require.Equal(t, "false", repo.values[SettingKeyTelegramBotEnabled])
}

func newManagedTelegramTestService(
	env config.TelegramConfig,
	repo *telegramRuntimeSettingRepo,
	encryptor SecretEncryptor,
	factory TelegramBotAPIFactory,
	bindings TelegramAdminBindingRepository,
) *TelegramBotService {
	if bindings == nil {
		bindings = newTelegramBindingStub()
	}
	cfg := &config.Config{Telegram: env}
	cfg.Totp.EncryptionKeyConfigured = true
	settings := NewSettingService(repo, cfg)
	users := telegramUserReaderStub{users: map[int64]*User{7: activeTelegramAdmin(7)}}
	return NewManagedTelegramBotService(
		cfg, bindings, newTelegramStateStub(), factory, repo, encryptor, settings, users, nil, nil, nil, nil,
	)
}

type telegramRuntimeEncryptor struct{}

func (telegramRuntimeEncryptor) Encrypt(plaintext string) (string, error) {
	return "cipher:" + base64.RawStdEncoding.EncodeToString([]byte(plaintext)), nil
}

func (telegramRuntimeEncryptor) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "cipher:") {
		return "", errors.New("invalid ciphertext")
	}
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(ciphertext, "cipher:"))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

type telegramRuntimeSettingRepo struct {
	values         map[string]string
	setMultipleErr error
}

func newTelegramRuntimeSettingRepo() *telegramRuntimeSettingRepo {
	return &telegramRuntimeSettingRepo{values: map[string]string{}}
}

func (r *telegramRuntimeSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *telegramRuntimeSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}
func (r *telegramRuntimeSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *telegramRuntimeSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *telegramRuntimeSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	if r.setMultipleErr != nil {
		return r.setMultipleErr
	}
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *telegramRuntimeSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return cloneTelegramRuntimeValues(r.values), nil
}

func (r *telegramRuntimeSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func cloneTelegramRuntimeValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

type telegramLifecycleBot struct {
	username        string
	getMeErr        error
	getMeCalls      int
	commandCalls    int
	menuCalls       int
	webhookCalls    int
	deleteCalls     int
	deleteErr       error
	webhookURL      string
	webhookSecret   string
	lastDropPending bool
}

func newTelegramLifecycleBot(username string) *telegramLifecycleBot {
	return &telegramLifecycleBot{username: username}
}

func telegramLifecycleFactory(bots map[string]*telegramLifecycleBot) TelegramBotAPIFactory {
	return func(token string) TelegramBotAPI {
		return bots[token]
	}
}

func (b *telegramLifecycleBot) GetMe(context.Context) (*TelegramUser, error) {
	b.getMeCalls++
	if b.getMeErr != nil {
		return nil, b.getMeErr
	}
	return &TelegramUser{ID: 1, IsBot: true, Username: b.username}, nil
}

func (b *telegramLifecycleBot) SetMyCommands(context.Context, []TelegramBotCommand) error {
	b.commandCalls++
	return nil
}

func (b *telegramLifecycleBot) SetChatMenuButton(context.Context) error {
	b.menuCalls++
	return nil
}
func (b *telegramLifecycleBot) SetWebhook(
	_ context.Context,
	webhookURL string,
	secret string,
	_ []string,
) error {
	b.webhookCalls++
	b.webhookURL = webhookURL
	b.webhookSecret = secret
	b.lastDropPending = false
	return nil
}

func (b *telegramLifecycleBot) DeleteWebhook(_ context.Context, dropPendingUpdates bool) error {
	b.deleteCalls++
	b.lastDropPending = dropPendingUpdates
	return b.deleteErr
}

func (*telegramLifecycleBot) SendMessage(
	context.Context, int64, string, *TelegramInlineKeyboardMarkup,
) error {
	return nil
}

func (*telegramLifecycleBot) EditMessageText(
	context.Context, int64, int, string, *TelegramInlineKeyboardMarkup,
) error {
	return nil
}

func (*telegramLifecycleBot) DeleteMessage(context.Context, int64, int) error {
	return nil
}

func (*telegramLifecycleBot) AnswerCallbackQuery(context.Context, string, string) error {
	return nil
}
