package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type telegramHandlerBindingRepoStub struct {
	bindings []*service.TelegramBinding
}

func (s *telegramHandlerBindingRepoStub) Bind(context.Context, int64, service.TelegramIdentity) (*service.TelegramBinding, error) {
	return nil, errors.New("unexpected binding")
}
func (s *telegramHandlerBindingRepoStub) ListActiveBindings(context.Context, int64) ([]*service.TelegramBinding, error) {
	return s.bindings, nil
}
func (s *telegramHandlerBindingRepoStub) GetActiveBindingByTelegramUserID(context.Context, int64) (*service.TelegramBinding, error) {
	return nil, service.ErrTelegramBindingNotFound
}
func (s *telegramHandlerBindingRepoStub) RevokeBinding(context.Context, int64, int64) (*service.TelegramBinding, error) {
	return nil, service.ErrTelegramBindingNotFound
}

type telegramHandlerStateStub struct {
	claims  int
	issue   *service.TelegramVerificationCode
	pending *service.TelegramVerificationCodeStatus
}

func (s *telegramHandlerStateStub) IssueVerificationCode(context.Context, int64) (*service.TelegramVerificationCode, error) {
	if s.issue == nil {
		return nil, errors.New("unexpected verification code issue")
	}
	return s.issue, nil
}
func (s *telegramHandlerStateStub) GetVerificationCodeStatus(context.Context, int64) (*service.TelegramVerificationCodeStatus, error) {
	return s.pending, nil
}
func (s *telegramHandlerStateStub) CancelVerificationCode(context.Context, int64) (bool, error) {
	return false, nil
}
func (s *telegramHandlerStateStub) ConsumeVerificationCode(context.Context, string) (int64, time.Duration, error) {
	return 0, 0, service.ErrTelegramVerificationCodeInvalid
}
func (s *telegramHandlerStateStub) RestoreVerificationCode(context.Context, string, int64, time.Duration) (bool, error) {
	return false, nil
}
func (s *telegramHandlerStateStub) AllowVerificationAttempt(context.Context, int64) (bool, error) {
	return true, nil
}
func (s *telegramHandlerStateStub) ClearVerificationAttempts(context.Context, int64) error {
	return nil
}
func (s *telegramHandlerStateStub) ClaimUpdate(context.Context, int64) (bool, error) {
	s.claims++
	return true, nil
}
func (s *telegramHandlerStateStub) CompleteUpdate(context.Context, int64) error { return nil }
func (s *telegramHandlerStateStub) ReleaseUpdate(context.Context, int64) error  { return nil }
func (s *telegramHandlerStateStub) AcquireConfigLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *telegramHandlerStateStub) ReleaseConfigLock(context.Context, string) error { return nil }
func (s *telegramHandlerStateStub) SetPendingSettingInput(context.Context, int64, service.TelegramPendingSettingInput) error {
	return nil
}
func (s *telegramHandlerStateStub) GetPendingSettingInput(context.Context, int64) (*service.TelegramPendingSettingInput, error) {
	return nil, nil
}
func (s *telegramHandlerStateStub) TakePendingSettingInput(context.Context, int64) (*service.TelegramPendingSettingInput, error) {
	return nil, nil
}
func (s *telegramHandlerStateStub) TakePendingSettingInputIfNonce(context.Context, int64, string) (*service.TelegramPendingSettingInput, error) {
	return nil, nil
}
func (s *telegramHandlerStateStub) DeletePendingSettingInput(context.Context, int64) (bool, error) {
	return false, nil
}

type telegramHandlerUserReaderStub struct{}

func (telegramHandlerUserReaderStub) GetByID(context.Context, int64) (*service.User, error) {
	return &service.User{ID: 7, Email: "admin@example.com", Role: service.RoleAdmin, Status: service.StatusActive}, nil
}

type telegramHandlerBotStub struct {
	answers int
	edits   int
}

func (s *telegramHandlerBotStub) GetMe(context.Context) (*service.TelegramUser, error) {
	return nil, nil
}
func (s *telegramHandlerBotStub) SetMyCommands(context.Context, []service.TelegramBotCommand) error {
	return nil
}
func (s *telegramHandlerBotStub) SetChatMenuButton(context.Context) error { return nil }
func (s *telegramHandlerBotStub) SetWebhook(context.Context, string, string, []string) error {
	return nil
}
func (s *telegramHandlerBotStub) DeleteWebhook(context.Context, bool) error { return nil }
func (s *telegramHandlerBotStub) SendMessage(context.Context, int64, string, *service.TelegramInlineKeyboardMarkup) error {
	return nil
}
func (s *telegramHandlerBotStub) EditMessageText(context.Context, int64, int, string, *service.TelegramInlineKeyboardMarkup) error {
	s.edits++
	return nil
}
func (s *telegramHandlerBotStub) DeleteMessage(context.Context, int64, int) error { return nil }
func (s *telegramHandlerBotStub) AnswerCallbackQuery(context.Context, string, string) error {
	s.answers++
	return nil
}

func newTelegramHandlerForTest(bindings *telegramHandlerBindingRepoStub, state *telegramHandlerStateStub, bot *telegramHandlerBotStub) *TelegramHandler {
	cfg := &config.Config{}
	cfg.Telegram = config.TelegramConfig{
		Enabled: true, BotToken: "123456:token-value", BotUsername: "test_admin_bot", WebhookSecret: "secret_value",
	}
	telegram := service.NewTelegramBotService(cfg, bindings, state, bot, nil, telegramHandlerUserReaderStub{}, nil, nil, nil, nil)
	return NewTelegramHandler(telegram)
}

type telegramHandlerTrackingBody struct {
	reads int
}

func (b *telegramHandlerTrackingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (*telegramHandlerTrackingBody) Close() error { return nil }

func TestTelegramWebhookChecksSecretBeforeParsingBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	bindings := &telegramHandlerBindingRepoStub{}
	state := &telegramHandlerStateStub{}
	bot := &telegramHandlerBotStub{}
	handler := newTelegramHandlerForTest(bindings, state, bot)
	router := gin.New()
	router.POST("/webhook", handler.Webhook)

	trackingBody := &telegramHandlerTrackingBody{}
	wrong := httptest.NewRequest("POST", "/webhook", nil)
	wrong.Body = trackingBody
	wrong.Header.Set(service.TelegramWebhookSecretHeader, "wrong")
	wrongRecorder := httptest.NewRecorder()
	router.ServeHTTP(wrongRecorder, wrong)
	require.Equal(t, 401, wrongRecorder.Code)
	require.Zero(t, trackingBody.reads)
	require.Zero(t, state.claims)

	validSecretInvalidBody := httptest.NewRequest("POST", "/webhook", strings.NewReader("not-json"))
	validSecretInvalidBody.Header.Set(service.TelegramWebhookSecretHeader, "secret_value")
	validRecorder := httptest.NewRecorder()
	router.ServeHTTP(validRecorder, validSecretInvalidBody)
	require.Equal(t, 400, validRecorder.Code)
	require.Zero(t, state.claims)
}

func TestTelegramCreateVerificationCodeResponseShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expiresAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	bindings := &telegramHandlerBindingRepoStub{}
	state := &telegramHandlerStateStub{issue: &service.TelegramVerificationCode{Code: "A1B2C3D4E5F6G7H", ExpiresAt: expiresAt}}
	handler := newTelegramHandlerForTest(bindings, state, &telegramHandlerBotStub{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Next()
	})
	router.POST("/verification-code", handler.CreateVerificationCode)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("POST", "/verification-code", nil))
	require.Equal(t, 200, recorder.Code)

	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Len(t, payload.Data, 2)
	require.Equal(t, "A1B2C3D4E5F6G7H", payload.Data["code"])
	require.Equal(t, expiresAt.Format(time.RFC3339), payload.Data["expires_at"])
	require.NotContains(t, recorder.Body.String(), "challenge")
	require.NotContains(t, recorder.Body.String(), "deep_link")
}

func TestTelegramStatusResponseUsesSafeServiceContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	expiresAt := time.Date(2030, time.February, 3, 4, 5, 6, 0, time.UTC)
	boundAt := expiresAt.Add(-time.Hour)
	bindings := &telegramHandlerBindingRepoStub{bindings: []*service.TelegramBinding{{
		ID: 9, AdminUserID: 7, AdminEmail: "admin@example.com", TelegramUserID: 42,
		PrivateChatID: 42, TelegramUsername: "operator", DisplayName: "Operator", BoundAt: boundAt,
	}}}
	state := &telegramHandlerStateStub{pending: &service.TelegramVerificationCodeStatus{ExpiresAt: expiresAt}}
	handler := newTelegramHandlerForTest(bindings, state, &telegramHandlerBotStub{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Next()
	})
	router.GET("/status", handler.Status)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("GET", "/status", nil))
	require.Equal(t, 200, recorder.Code)

	var payload struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, expiresAt.Format(time.RFC3339), payload.Data["pending_expires_at"])
	require.NotContains(t, payload.Data, "pending_challenge_expires_at")
	items, ok := payload.Data["bindings"].([]any)
	require.True(t, ok)
	require.Len(t, items, 1)
	binding, ok := items[0].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, binding, "admin_user_id")
	require.NotContains(t, binding, "admin_email")
	require.NotContains(t, binding, "private_chat_id")
}

func TestTelegramCallbackAcknowledgedButRevokedBindingCannotAuthorize(t *testing.T) {
	bindings := &telegramHandlerBindingRepoStub{}
	state := &telegramHandlerStateStub{}
	bot := &telegramHandlerBotStub{}
	cfg := &config.Config{}
	cfg.Telegram = config.TelegramConfig{Enabled: true, BotToken: "token", BotUsername: "test_admin_bot", WebhookSecret: "secret"}
	telegram := service.NewTelegramBotService(cfg, bindings, state, bot, nil, telegramHandlerUserReaderStub{}, nil, nil, nil, nil)

	err := telegram.ProcessUpdate(context.Background(), &service.TelegramUpdate{
		UpdateID: 12,
		CallbackQuery: &service.TelegramCallbackQuery{
			ID: "callback-1", From: service.TelegramUser{ID: 42}, Data: "s",
			Message: &service.TelegramMessage{MessageID: 9, Chat: service.TelegramChat{ID: 42, Type: "private"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 1, bot.answers)
	require.Equal(t, 1, bot.edits)
	require.Equal(t, 1, state.claims)
}
