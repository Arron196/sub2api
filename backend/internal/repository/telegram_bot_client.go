package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const telegramBotAPIBaseURL = "https://api.telegram.org"

const (
	telegramHTTPTimeout = 10 * time.Second
	telegramMaxResponse = 1 << 20
)

type telegramBotClient struct {
	token  string
	client *http.Client
}

func NewTelegramBotClientFactory() service.TelegramBotAPIFactory {
	httpClient := &http.Client{Timeout: telegramHTTPTimeout}
	return func(token string) service.TelegramBotAPI {
		return &telegramBotClient{token: token, client: httpClient}
	}
}

func NewTelegramBotClient(cfg *config.Config) service.TelegramBotAPI {
	var token string
	if cfg != nil {
		token = cfg.Telegram.BotToken
	}
	return NewTelegramBotClientFactory()(token)
}

type telegramAPIResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
}

func (c *telegramBotClient) call(ctx context.Context, method string, payload any, result any) error {
	if c == nil || c.token == "" {
		return errors.New("telegram bot client is not configured")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal Telegram %s request: %w", method, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		telegramBotAPIBaseURL+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create Telegram %s request", method)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("Telegram %s request failed", method)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, telegramMaxResponse+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil || len(responseBody) > telegramMaxResponse {
		return fmt.Errorf("Telegram %s response is invalid", method)
	}
	var envelope telegramAPIResponse
	if err := json.Unmarshal(responseBody, &envelope); err != nil || !envelope.OK || response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Telegram %s API call failed", method)
	}
	if result != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, result); err != nil {
			return fmt.Errorf("decode Telegram %s response", method)
		}
	}
	return nil
}

func (c *telegramBotClient) GetMe(ctx context.Context) (*service.TelegramUser, error) {
	user := &service.TelegramUser{}
	if err := c.call(ctx, "getMe", struct{}{}, user); err != nil {
		return nil, err
	}
	return user, nil
}

func (c *telegramBotClient) SetMyCommands(ctx context.Context, commands []service.TelegramBotCommand) error {
	return c.call(ctx, "setMyCommands", struct {
		Commands []service.TelegramBotCommand `json:"commands"`
	}{Commands: commands}, nil)
}
func (c *telegramBotClient) SetChatMenuButton(ctx context.Context) error {
	return c.call(ctx, "setChatMenuButton", struct {
		MenuButton map[string]string `json:"menu_button"`
	}{MenuButton: map[string]string{"type": "commands"}}, nil)
}

func (c *telegramBotClient) SetWebhook(ctx context.Context, webhookURL, secret string, allowedUpdates []string) error {
	return c.call(ctx, "setWebhook", struct {
		URL                string   `json:"url"`
		SecretToken        string   `json:"secret_token"`
		AllowedUpdates     []string `json:"allowed_updates"`
		DropPendingUpdates bool     `json:"drop_pending_updates"`
	}{
		URL: webhookURL, SecretToken: secret, AllowedUpdates: allowedUpdates,
		DropPendingUpdates: false,
	}, nil)
}

func (c *telegramBotClient) DeleteWebhook(ctx context.Context, dropPendingUpdates bool) error {
	return c.call(ctx, "deleteWebhook", struct {
		DropPendingUpdates bool `json:"drop_pending_updates"`
	}{DropPendingUpdates: dropPendingUpdates}, nil)
}

func (c *telegramBotClient) SendMessage(ctx context.Context, chatID int64, text string, markup *service.TelegramInlineKeyboardMarkup) error {
	return c.call(ctx, "sendMessage", struct {
		ChatID      int64                                 `json:"chat_id"`
		Text        string                                `json:"text"`
		ReplyMarkup *service.TelegramInlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, Text: text, ReplyMarkup: markup}, nil)
}

func (c *telegramBotClient) EditMessageText(ctx context.Context, chatID int64, messageID int, text string, markup *service.TelegramInlineKeyboardMarkup) error {
	return c.call(ctx, "editMessageText", struct {
		ChatID      int64                                 `json:"chat_id"`
		MessageID   int                                   `json:"message_id"`
		Text        string                                `json:"text"`
		ReplyMarkup *service.TelegramInlineKeyboardMarkup `json:"reply_markup,omitempty"`
	}{ChatID: chatID, MessageID: messageID, Text: text, ReplyMarkup: markup}, nil)
}

func (c *telegramBotClient) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return c.call(ctx, "deleteMessage", struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int   `json:"message_id"`
	}{ChatID: chatID, MessageID: messageID}, nil)
}

func (c *telegramBotClient) AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string) error {
	return c.call(ctx, "answerCallbackQuery", struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}{CallbackQueryID: callbackQueryID, Text: text}, nil)
}
