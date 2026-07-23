package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAIAgentModelProtocols(t *testing.T) {
	tests := []struct {
		name         string
		protocol     string
		thinkingMode string
		path         string
		response     string
		assertBody   func(*testing.T, map[string]any)
	}{
		{
			name: "chat completions", protocol: agentProtocolChatCompletions, thinkingMode: "xhigh", path: "/v1/chat/completions",
			response: `{"choices":[{"message":{"role":"assistant","content":"chat ok"}}]}`,
			assertBody: func(t *testing.T, body map[string]any) {
				if body["reasoning_effort"] != "xhigh" {
					t.Errorf("reasoning_effort = %#v", body["reasoning_effort"])
				}
			},
		},
		{
			name: "responses", protocol: agentProtocolResponses, thinkingMode: "xhigh", path: "/v1/responses",
			response: `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"responses ok"}]}]}`,
			assertBody: func(t *testing.T, body map[string]any) {
				reasoning, _ := body["reasoning"].(map[string]any)
				if reasoning["effort"] != "xhigh" {
					t.Errorf("reasoning.effort = %#v", reasoning["effort"])
				}
			},
		},
		{
			name: "messages", protocol: agentProtocolMessages, thinkingMode: "4096", path: "/v1/messages",
			response: `{"content":[{"type":"text","text":"messages ok"}]}`,
			assertBody: func(t *testing.T, body map[string]any) {
				thinking, _ := body["thinking"].(map[string]any)
				if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(4096) {
					t.Errorf("thinking = %#v", thinking)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.path {
					t.Errorf("path = %q, want %q", request.URL.Path, test.path)
				}
				if test.protocol == agentProtocolMessages {
					if request.Header.Get("x-api-key") != "model-key" || request.Header.Get("anthropic-version") == "" {
						t.Errorf("missing Messages authentication headers")
					}
					if request.Header.Get("Authorization") != "" {
						t.Errorf("Messages request must not use Bearer authentication")
					}
				} else if request.Header.Get("Authorization") != "Bearer model-key" {
					t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
				}
				var body map[string]any
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				test.assertBody(t, body)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.response))
			}))
			defer server.Close()

			service := &AIAgentService{client: server.Client()}
			message, err := service.complete(context.Background(), AIAgentConfig{
				BaseURL: server.URL, Model: "test-model", Protocol: test.protocol, ThinkingMode: test.thinkingMode,
			}, "model-key", []agentModelMessage{{Role: "user", Content: "hello"}})
			if err != nil {
				t.Fatalf("complete() error = %v", err)
			}
			if text := modelMessageText(message.Content); !strings.Contains(text, "ok") {
				t.Fatalf("content = %q", text)
			}
		})
	}
}

func TestResponsesPreservesReasoningItemsAcrossToolCalls(t *testing.T) {
	reasoning := json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`)
	functionCall := json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"call_1","name":"search_admin_operations","arguments":"{\"query\":\"users\"}"}`)
	history := []agentModelMessage{
		{Role: "assistant", ResponsesOutput: []json.RawMessage{reasoning, functionCall}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"status":"success"}`},
	}
	encoded, err := json.Marshal(responsesInput(history))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	payload := string(encoded)
	for _, expected := range []string{`"encrypted_content":"opaque"`, `"type":"function_call"`, `"type":"function_call_output"`} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("responses input does not preserve %s: %s", expected, payload)
		}
	}
}

func TestMessagesPreservesSignedThinkingBlocksAcrossToolCalls(t *testing.T) {
	thinking := json.RawMessage(`{"type":"thinking","thinking":"private chain","signature":"signed-value"}`)
	toolUse := json.RawMessage(`{"type":"tool_use","id":"tool_1","name":"search_admin_operations","input":{"query":"users"}}`)
	history := []agentModelMessage{
		{Role: "assistant", AnthropicContent: []json.RawMessage{thinking, toolUse}},
		{Role: "tool", ToolCallID: "tool_1", Content: `{"status":"success"}`},
	}
	encoded, err := json.Marshal(anthropicMessages(history))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	payload := string(encoded)
	for _, expected := range []string{`"signature":"signed-value"`, `"type":"thinking"`, `"type":"tool_result"`} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("Messages input does not preserve %s: %s", expected, payload)
		}
	}
}
