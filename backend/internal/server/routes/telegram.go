package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// RegisterTelegramRoutes registers the unauthenticated Telegram webhook.
// The handler performs disabled/configuration checks and secret validation before parsing JSON.
func RegisterTelegramRoutes(v1 *gin.RouterGroup, telegram *handler.TelegramHandler) {
	v1.POST("/telegram/webhook", telegram.Webhook)
}
