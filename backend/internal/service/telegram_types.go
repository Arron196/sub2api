package service

import (
	"context"
	"errors"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	TelegramWebhookSecretHeader      = "X-Telegram-Bot-Api-Secret-Token"
	TelegramVerificationCodeLength   = 15
	TelegramVerificationCodeTTL      = 5 * time.Minute
	TelegramVerificationAttemptTTL   = 5 * time.Minute
	TelegramVerificationAttemptLimit = 10
	TelegramUpdateDeduplicationTTL   = 24 * time.Hour
	TelegramUpdateProcessingTTL      = time.Minute
	TelegramPendingSettingTTL        = 10 * time.Minute
	TelegramConfigLockTTL            = 2 * time.Minute
	TelegramGroupPageSize            = 6
)

const (
	TelegramGroupRateKindBase  = "base"
	TelegramGroupRateKindImage = "image"
	TelegramGroupRateKindVideo = "video"
	TelegramGroupRateKindPeak  = "peak"
)

var (
	ErrTelegramVerificationCodeInvalid = errors.New("telegram verification code is invalid")
	ErrTelegramIdentityInvalid         = errors.New("telegram identity is invalid")
	ErrTelegramPendingInputInvalid     = errors.New("telegram pending setting input is invalid")
	ErrTelegramGroupChanged            = errors.New("telegram group changed during rate update")
	ErrTelegramUnavailable             = infraerrors.New(503, "TELEGRAM_UNAVAILABLE", "Telegram admin bot is not configured")
	ErrTelegramBindingNotFound         = infraerrors.New(404, "TELEGRAM_BINDING_NOT_FOUND", "Telegram binding not found")
)

type TelegramBinding struct {
	ID               int64      `json:"id"`
	AdminUserID      int64      `json:"admin_user_id"`
	AdminEmail       string     `json:"admin_email,omitempty"`
	TelegramUserID   int64      `json:"telegram_user_id"`
	PrivateChatID    int64      `json:"-"`
	TelegramUsername string     `json:"username,omitempty"`
	DisplayName      string     `json:"display_name,omitempty"`
	BoundAt          time.Time  `json:"bound_at"`
	UpdatedAt        time.Time  `json:"-"`
	RevokedAt        *time.Time `json:"-"`
}

type TelegramIdentity struct {
	UserID        int64
	PrivateChatID int64
	Username      string
	DisplayName   string
}

type TelegramVerificationCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

type TelegramVerificationCodeStatus struct {
	ExpiresAt time.Time `json:"expires_at"`
}

type TelegramSettingInputType string

const (
	TelegramSettingTypeBool  TelegramSettingInputType = "bool"
	TelegramSettingTypeInt   TelegramSettingInputType = "int"
	TelegramSettingTypeFloat TelegramSettingInputType = "float"
	TelegramSettingTypeJSON  TelegramSettingInputType = "json"
	TelegramSettingTypeText  TelegramSettingInputType = "text"
	TelegramSettingTypeURL   TelegramSettingInputType = "url"
	TelegramSettingTypeEnum  TelegramSettingInputType = "enum"
)

type TelegramPendingSettingInput struct {
	Flow            string                   `json:"flow,omitempty"`
	SettingKey      string                   `json:"setting_key"`
	SettingID       string                   `json:"setting_id,omitempty"`
	InputType       TelegramSettingInputType `json:"input_type"`
	Stage           string                   `json:"stage,omitempty"`
	Candidate       string                   `json:"candidate,omitempty"`
	Category        string                   `json:"category,omitempty"`
	GroupID         int64                    `json:"group_id,omitempty"`
	RateKind        string                   `json:"rate_kind,omitempty"`
	ReturnPage      int                      `json:"return_page,omitempty"`
	OriginChatID    int64                    `json:"origin_chat_id,omitempty"`
	OriginMessageID int                      `json:"origin_message_id,omitempty"`
	OperationNonce  string                   `json:"operation_nonce,omitempty"`
	BindingID       int64                    `json:"binding_id,omitempty"`
	AdminUserID     int64                    `json:"admin_user_id,omitempty"`
	GroupUpdatedAt  time.Time                `json:"group_updated_at,omitempty"`
	ExpiresAt       time.Time                `json:"expires_at"`
}

type TelegramGroupRateMutation struct {
	Binding *TelegramBinding
	Admin   *User
	Group   *Group
	Kind    string
	Before  float64
	After   float64
}

type TelegramGroupRateMutationRepository interface {
	UpdateAuthorizedGroupRateMultiplier(
		ctx context.Context,
		bindingID int64,
		identity TelegramIdentity,
		groupID int64,
		kind string,
		multiplier float64,
		expectedUpdatedAt time.Time,
	) (*TelegramGroupRateMutation, error)
}

type TelegramAdminStatus struct {
	Configured        bool               `json:"configured"`
	Enabled           bool               `json:"enabled"`
	BotUsername       string             `json:"bot_username,omitempty"`
	WebhookConfigured bool               `json:"webhook_configured"`
	PendingExpiresAt  *time.Time         `json:"pending_verification_expires_at,omitempty"`
	Bindings          []*TelegramBinding `json:"bindings"`
}

type TelegramAdminBindingRepository interface {
	Bind(ctx context.Context, adminUserID int64, identity TelegramIdentity) (*TelegramBinding, error)
	ListActiveBindings(ctx context.Context, adminUserID int64) ([]*TelegramBinding, error)
	GetActiveBindingByTelegramUserID(ctx context.Context, telegramUserID int64) (*TelegramBinding, error)
	RevokeBinding(ctx context.Context, bindingID, adminUserID int64) (*TelegramBinding, error)
}

type TelegramAdminUserReader interface {
	GetByID(ctx context.Context, id int64) (*User, error)
}

type TelegramStateRepository interface {
	IssueVerificationCode(ctx context.Context, adminUserID int64) (*TelegramVerificationCode, error)
	GetVerificationCodeStatus(ctx context.Context, adminUserID int64) (*TelegramVerificationCodeStatus, error)
	CancelVerificationCode(ctx context.Context, adminUserID int64) (bool, error)
	ConsumeVerificationCode(ctx context.Context, code string) (int64, time.Duration, error)
	RestoreVerificationCode(ctx context.Context, code string, adminUserID int64, ttl time.Duration) (bool, error)
	AllowVerificationAttempt(ctx context.Context, telegramUserID int64) (bool, error)
	ClearVerificationAttempts(ctx context.Context, telegramUserID int64) error
	ClaimUpdate(ctx context.Context, updateID int64) (bool, error)
	CompleteUpdate(ctx context.Context, updateID int64) error
	ReleaseUpdate(ctx context.Context, updateID int64) error
	AcquireConfigLock(ctx context.Context, owner string, ttl time.Duration) (bool, error)
	ReleaseConfigLock(ctx context.Context, owner string) error
	SetPendingSettingInput(ctx context.Context, telegramUserID int64, input TelegramPendingSettingInput) error
	GetPendingSettingInput(ctx context.Context, telegramUserID int64) (*TelegramPendingSettingInput, error)
	TakePendingSettingInput(ctx context.Context, telegramUserID int64) (*TelegramPendingSettingInput, error)
	TakePendingSettingInputIfNonce(ctx context.Context, telegramUserID int64, nonce string) (*TelegramPendingSettingInput, error)
	DeletePendingSettingInput(ctx context.Context, telegramUserID int64) (bool, error)
}

type TelegramBotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type TelegramInlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type TelegramInlineKeyboardMarkup struct {
	InlineKeyboard [][]TelegramInlineKeyboardButton `json:"inline_keyboard"`
}

type TelegramBotAPIFactory func(token string) TelegramBotAPI

type TelegramBotAPI interface {
	GetMe(ctx context.Context) (*TelegramUser, error)
	SetMyCommands(ctx context.Context, commands []TelegramBotCommand) error
	SetChatMenuButton(ctx context.Context) error
	SetWebhook(ctx context.Context, webhookURL, secret string, allowedUpdates []string) error
	DeleteWebhook(ctx context.Context, dropPendingUpdates bool) error
	SendMessage(ctx context.Context, chatID int64, text string, markup *TelegramInlineKeyboardMarkup) error
	EditMessageText(ctx context.Context, chatID int64, messageID int, text string, markup *TelegramInlineKeyboardMarkup) error
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
	AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string) error
}

type TelegramUpdate struct {
	UpdateID      int64                  `json:"update_id"`
	Message       *TelegramMessage       `json:"message,omitempty"`
	CallbackQuery *TelegramCallbackQuery `json:"callback_query,omitempty"`
}

type TelegramMessage struct {
	MessageID int           `json:"message_id"`
	From      *TelegramUser `json:"from,omitempty"`
	Chat      TelegramChat  `json:"chat"`
	Text      string        `json:"text,omitempty"`
}

type TelegramCallbackQuery struct {
	ID      string           `json:"id"`
	From    TelegramUser     `json:"from"`
	Message *TelegramMessage `json:"message,omitempty"`
	Data    string           `json:"data,omitempty"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
