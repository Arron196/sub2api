package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const telegramWebhookMaxBodySize = 256 * 1024

type telegramAdminStatusResponse struct {
	Configured        bool                           `json:"configured"`
	Enabled           bool                           `json:"enabled"`
	BotUsername       string                         `json:"bot_username"`
	WebhookConfigured bool                           `json:"webhook_configured"`
	PendingExpiresAt  *time.Time                     `json:"pending_expires_at,omitempty"`
	Bindings          []telegramAdminBindingResponse `json:"bindings"`
}

type telegramAdminBindingResponse struct {
	ID               int64     `json:"id"`
	TelegramUserID   int64     `json:"telegram_user_id"`
	TelegramUsername string    `json:"username,omitempty"`
	DisplayName      string    `json:"display_name,omitempty"`
	BoundAt          time.Time `json:"bound_at"`
}

type telegramConfigUpdateRequest struct {
	Enabled    bool   `json:"enabled"`
	BotToken   string `json:"bot_token"`
	WebhookURL string `json:"webhook_url"`
}

type TelegramHandler struct {
	telegram *service.TelegramBotService
}

func NewTelegramHandler(telegram *service.TelegramBotService) *TelegramHandler {
	return &TelegramHandler{telegram: telegram}
}

func (h *TelegramHandler) GetConfig(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	configView, err := h.telegram.GetConfig(c.Request.Context(), subject.UserID)
	if err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	response.Success(c, configView)
}

func (h *TelegramHandler) UpdateConfig(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var request telegramConfigUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		response.BadRequest(c, "Invalid Telegram bot configuration")
		return
	}
	configView, err := h.telegram.UpdateConfig(c.Request.Context(), subject.UserID, service.TelegramBotConfigInput{
		Enabled: request.Enabled, BotToken: request.BotToken, WebhookURL: request.WebhookURL,
	})
	if err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	response.Success(c, configView)
}

func (h *TelegramHandler) Status(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	status, err := h.telegram.AdminStatus(c.Request.Context(), subject.UserID)
	if err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	bindings := make([]telegramAdminBindingResponse, 0, len(status.Bindings))
	for _, binding := range status.Bindings {
		if binding == nil {
			continue
		}
		bindings = append(bindings, telegramAdminBindingResponse{
			ID:               binding.ID,
			TelegramUserID:   binding.TelegramUserID,
			TelegramUsername: binding.TelegramUsername,
			DisplayName:      binding.DisplayName,
			BoundAt:          binding.BoundAt,
		})
	}
	response.Success(c, telegramAdminStatusResponse{
		Configured:        status.Configured,
		Enabled:           status.Enabled,
		BotUsername:       status.BotUsername,
		WebhookConfigured: status.WebhookConfigured,
		PendingExpiresAt:  status.PendingExpiresAt,
		Bindings:          bindings,
	})
}

func (h *TelegramHandler) CreateVerificationCode(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	code, err := h.telegram.IssueVerificationCode(c.Request.Context(), subject.UserID)
	if err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	response.Success(c, code)
}

func (h *TelegramHandler) CancelVerificationCode(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if err := h.telegram.CancelVerificationCode(c.Request.Context(), subject.UserID); err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *TelegramHandler) DeleteBinding(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	bindingID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || bindingID <= 0 {
		response.BadRequest(c, "Invalid Telegram binding ID")
		return
	}
	if err := h.telegram.RevokeBinding(c.Request.Context(), subject.UserID, bindingID); err != nil {
		writeTelegramHandlerError(c, err)
		return
	}
	response.Success(c, nil)
}

func (h *TelegramHandler) Webhook(c *gin.Context) {
	session, enabled, configured := h.telegram.AuthorizeWebhook(
		c.Request.Context(),
		c.GetHeader(service.TelegramWebhookSecretHeader),
	)
	if !enabled {
		c.Status(http.StatusNotFound)
		return
	}
	if !configured {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if session == nil {
		c.Status(http.StatusUnauthorized)
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, telegramWebhookMaxBodySize)
	decoder := json.NewDecoder(c.Request.Body)
	var update *service.TelegramUpdate
	if err := decoder.Decode(&update); err != nil || update == nil {
		response.BadRequest(c, "Invalid Telegram update")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		response.BadRequest(c, "Invalid Telegram update")
		return
	}
	if update.UpdateID < 0 {
		response.BadRequest(c, "Invalid Telegram update")
		return
	}
	if err := h.telegram.ProcessWebhookUpdate(c.Request.Context(), session, update); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusOK)
}

func writeTelegramHandlerError(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, service.ErrTelegramUnavailable) ||
		errors.Is(err, service.ErrTelegramBindingNotFound) ||
		errors.Is(err, service.ErrTelegramConfigInvalid) ||
		errors.Is(err, service.ErrTelegramEncryptionKeyRequired) ||
		errors.Is(err, service.ErrTelegramConfigBusy) {
		response.ErrorFrom(c, err)
		return
	}
	response.InternalError(c, "Telegram operation failed")
}
