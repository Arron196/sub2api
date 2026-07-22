package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestTelegramConfigValidationIsIsolatedWhenDisabled(t *testing.T) {
	cfg := TelegramConfig{}
	require.NoError(t, cfg.Validate())
}

func TestTelegramConfigNormalizeAndValidateEnabled(t *testing.T) {
	cfg := TelegramConfig{
		Enabled:       true,
		BotToken:      " token-value ",
		BotUsername:   " @example_admin_bot ",
		WebhookURL:    " https://example.com/api/v1/telegram/webhook ",
		WebhookSecret: " secret_value ",
	}
	cfg.Normalize()
	require.Equal(t, "token-value", cfg.BotToken)
	require.Equal(t, "example_admin_bot", cfg.BotUsername)
	require.Equal(t, "https://example.com/api/v1/telegram/webhook", cfg.WebhookURL)
	require.Equal(t, "secret_value", cfg.WebhookSecret)
	require.NoError(t, cfg.Validate())

	cfg.WebhookURL = "http://example.com/hook"
	require.ErrorContains(t, cfg.Validate(), "absolute HTTPS URL")
	cfg.WebhookURL = "https://example.com/hook"
	cfg.WebhookSecret = "not allowed"
	require.ErrorContains(t, cfg.Validate(), "webhook_secret")
}

func TestTelegramConfigLoadsFromEnvironmentOnlyAndTrimsValues(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("TELEGRAM_ENABLED", "true")
	t.Setenv("TELEGRAM_BOT_TOKEN", " token-value ")
	t.Setenv("TELEGRAM_BOT_USERNAME", " @example_admin_bot ")
	t.Setenv("TELEGRAM_WEBHOOK_URL", " https://example.com/api/v1/telegram/webhook ")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", " secret_value ")

	viper.AutomaticEnv()
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	setDefaults()
	var cfg Config
	require.NoError(t, viper.Unmarshal(&cfg))
	cfg.Telegram.Normalize()

	require.True(t, cfg.Telegram.Enabled)
	require.Equal(t, "token-value", cfg.Telegram.BotToken)
	require.Equal(t, "example_admin_bot", cfg.Telegram.BotUsername)
	require.Equal(t, "https://example.com/api/v1/telegram/webhook", cfg.Telegram.WebhookURL)
	require.Equal(t, "secret_value", cfg.Telegram.WebhookSecret)
	require.NoError(t, cfg.Telegram.Validate())
}
