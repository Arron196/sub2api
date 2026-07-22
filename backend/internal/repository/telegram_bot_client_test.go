package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type telegramRoundTripFunc func(*http.Request) (*http.Response, error)

func (f telegramRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestTelegramBotClientDeleteMessageUsesSafeGenericCall(t *testing.T) {
	var method, path string
	var payload struct {
		ChatID    int64 `json:"chat_id"`
		MessageID int   `json:"message_id"`
	}
	client := &telegramBotClient{token: "sensitive-token", client: &http.Client{
		Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			method, path = request.Method, request.URL.Path
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			return nil, errors.New(request.URL.String() + " payload-secret")
		}),
	}}

	err := client.DeleteMessage(context.Background(), 88001, 42)
	require.Error(t, err)
	require.Equal(t, http.MethodPost, method)
	require.Equal(t, "/botsensitive-token/deleteMessage", path)
	require.Equal(t, int64(88001), payload.ChatID)
	require.Equal(t, 42, payload.MessageID)
	require.NotContains(t, err.Error(), "sensitive-token")
	require.NotContains(t, err.Error(), "payload-secret")
}

func TestTelegramBotClientWebhookLifecyclePayloadsAndErrorsAreSanitized(t *testing.T) {
	var setPayload struct {
		URL                string   `json:"url"`
		SecretToken        string   `json:"secret_token"`
		AllowedUpdates     []string `json:"allowed_updates"`
		DropPendingUpdates bool     `json:"drop_pending_updates"`
	}
	var deletePayload struct {
		DropPendingUpdates bool `json:"drop_pending_updates"`
	}
	client := &telegramBotClient{token: "sensitive-token", client: &http.Client{
		Transport: telegramRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/botsensitive-token/setWebhook":
				require.NoError(t, json.NewDecoder(request.Body).Decode(&setPayload))
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(
					`{"ok":false,"description":"secret-value at https://example.com/hook sensitive-token"}`,
				))}, nil
			case "/botsensitive-token/deleteWebhook":
				require.NoError(t, json.NewDecoder(request.Body).Decode(&deletePayload))
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
					`{"ok":true,"result":true}`,
				))}, nil
			default:
				t.Fatalf("unexpected Telegram path %s", request.URL.Path)
				return nil, nil
			}
		}),
	}}
	err := client.SetWebhook(
		context.Background(), "https://example.com/hook", "secret-value",
		[]string{"message", "callback_query"},
	)
	require.Error(t, err)
	require.Equal(t, "https://example.com/hook", setPayload.URL)
	require.Equal(t, "secret-value", setPayload.SecretToken)
	require.Equal(t, []string{"message", "callback_query"}, setPayload.AllowedUpdates)
	require.False(t, setPayload.DropPendingUpdates)
	for _, sensitive := range []string{
		"sensitive-token", "secret-value", "https://example.com/hook",
	} {
		require.NotContains(t, err.Error(), sensitive)
	}

	require.NoError(t, client.DeleteWebhook(context.Background(), false))
	require.False(t, deletePayload.DropPendingUpdates)
}
