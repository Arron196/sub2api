package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	agentProtocolChatCompletions = "chat_completions"
	agentProtocolResponses       = "responses"
	agentProtocolMessages        = "messages"
)

func setAgentModelHeaders(request *http.Request, protocol, key string) {
	request.Header.Set("Content-Type", "application/json")
	if protocol == agentProtocolMessages {
		request.Header.Set("x-api-key", key)
		request.Header.Set("anthropic-version", "2023-06-01")
		return
	}
	request.Header.Set("Authorization", "Bearer "+key)
}

func (s *AIAgentService) sendModelRequest(ctx context.Context, config AIAgentConfig, key, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.modelBaseURL(config)+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setAgentModelHeaders(request, config.Protocol, key)
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Agent model: %w", err)
	}
	defer response.Body.Close()
	return readAgentResponse(response, 4<<20)
}

func (s *AIAgentService) completeChatCompletions(ctx context.Context, config AIAgentConfig, key string, history []agentModelMessage) (agentModelMessage, error) {
	messages := make([]agentModelMessage, 0, len(history)+1)
	messages = append(messages, agentModelMessage{Role: "system", Content: agentSystemPrompt})
	messages = append(messages, history...)
	payload := map[string]any{
		"model":       config.Model,
		"messages":    messages,
		"tools":       agentTools,
		"tool_choice": "auto",
	}
	if config.ThinkingMode != "" {
		payload["reasoning_effort"] = config.ThinkingMode
	}
	responseBody, err := s.sendModelRequest(ctx, config, key, "/v1/chat/completions", payload)
	if err != nil {
		return agentModelMessage{}, err
	}
	var completion agentCompletionResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil || len(completion.Choices) == 0 {
		return agentModelMessage{}, errors.New("Agent Chat Completions response is invalid")
	}
	return completion.Choices[0].Message, nil
}

func (s *AIAgentService) completeResponses(ctx context.Context, config AIAgentConfig, key string, history []agentModelMessage) (agentModelMessage, error) {
	payload := map[string]any{
		"model":             config.Model,
		"instructions":      agentSystemPrompt,
		"input":             responsesInput(history),
		"tools":             responsesTools(),
		"tool_choice":       "auto",
		"max_output_tokens": 4096,
	}
	if config.ThinkingMode != "" {
		payload["reasoning"] = map[string]any{"effort": config.ThinkingMode, "summary": "auto"}
		payload["include"] = []string{"reasoning.encrypted_content"}
	}
	responseBody, err := s.sendModelRequest(ctx, config, key, "/v1/responses", payload)
	if err != nil {
		return agentModelMessage{}, err
	}
	var response struct {
		Output []json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil || len(response.Output) == 0 {
		return agentModelMessage{}, errors.New("Agent Responses response is invalid")
	}
	message := agentModelMessage{Role: "assistant", ResponsesOutput: response.Output}
	var textParts []string
	for _, raw := range response.Output {
		var item struct {
			ID        string          `json:"id"`
			Type      string          `json:"type"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Content   json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		switch item.Type {
		case "function_call":
			callID := item.CallID
			if callID == "" {
				callID = item.ID
			}
			message.ToolCalls = append(message.ToolCalls, agentToolCall{ID: callID, Type: "function", Function: agentToolFunction{Name: item.Name, Arguments: item.Arguments}})
		case "message":
			var blocks []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			}
			if json.Unmarshal(item.Content, &blocks) == nil {
				for _, block := range blocks {
					if block.Type == "output_text" && block.Text != "" {
						textParts = append(textParts, block.Text)
					} else if block.Type == "refusal" && block.Refusal != "" {
						textParts = append(textParts, block.Refusal)
					}
				}
			}
		}
	}
	message.Content = strings.Join(textParts, "\n")
	if len(message.ToolCalls) == 0 && strings.TrimSpace(strings.Join(textParts, "")) == "" {
		return agentModelMessage{}, errors.New("Agent Responses response contained no text or tool calls")
	}
	return message, nil
}

func responsesInput(history []agentModelMessage) []any {
	input := make([]any, 0, len(history))
	for _, message := range history {
		switch message.Role {
		case "user":
			input = append(input, map[string]any{"role": "user", "content": modelMessageText(message.Content)})
		case "assistant":
			if len(message.ResponsesOutput) > 0 {
				for _, item := range message.ResponsesOutput {
					input = append(input, item)
				}
				continue
			}
			if content := modelMessageText(message.Content); content != "" {
				input = append(input, map[string]any{"role": "assistant", "content": content})
			}
			for _, call := range message.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message.ToolCallID, "output": modelMessageText(message.Content)})
		}
	}
	return input
}

func responsesTools() []map[string]any {
	tools := make([]map[string]any, 0, len(agentTools))
	for _, tool := range agentTools {
		function, _ := tool["function"].(map[string]any)
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        function["name"],
			"description": function["description"],
			"parameters":  function["parameters"],
		})
	}
	return tools
}

func (s *AIAgentService) completeMessages(ctx context.Context, config AIAgentConfig, key string, history []agentModelMessage) (agentModelMessage, error) {
	maxTokens := 4096
	payload := map[string]any{
		"model":       config.Model,
		"system":      agentSystemPrompt,
		"messages":    anthropicMessages(history),
		"tools":       anthropicTools(),
		"tool_choice": map[string]any{"type": "auto"},
	}
	if config.ThinkingMode != "" {
		if budget, err := strconv.Atoi(config.ThinkingMode); err == nil {
			if budget < 1024 || budget > 128000 {
				return agentModelMessage{}, errors.New("Messages thinking budget must be between 1024 and 128000 tokens")
			}
			payload["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			maxTokens = budget + 4096
		} else {
			payload["thinking"] = map[string]any{"type": config.ThinkingMode}
			maxTokens = 16384
		}
	}
	payload["max_tokens"] = maxTokens
	responseBody, err := s.sendModelRequest(ctx, config, key, "/v1/messages", payload)
	if err != nil {
		return agentModelMessage{}, err
	}
	var response struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil || len(response.Content) == 0 {
		return agentModelMessage{}, errors.New("Agent Messages response is invalid")
	}
	message := agentModelMessage{Role: "assistant", AnthropicContent: response.Content}
	var textParts []string
	for _, raw := range response.Content {
		var block struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			arguments := string(block.Input)
			if arguments == "" {
				arguments = "{}"
			}
			message.ToolCalls = append(message.ToolCalls, agentToolCall{ID: block.ID, Type: "function", Function: agentToolFunction{Name: block.Name, Arguments: arguments}})
		}
	}
	message.Content = strings.Join(textParts, "\n")
	if len(message.ToolCalls) == 0 && strings.TrimSpace(strings.Join(textParts, "")) == "" {
		return agentModelMessage{}, errors.New("Agent Messages response contained no text or tool calls")
	}
	return message, nil
}

func anthropicMessages(history []agentModelMessage) []map[string]any {
	messages := make([]map[string]any, 0, len(history))
	appendMessage := func(role string, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if len(messages) > 0 && messages[len(messages)-1]["role"] == role {
			existing, _ := messages[len(messages)-1]["content"].([]any)
			messages[len(messages)-1]["content"] = append(existing, blocks...)
			return
		}
		messages = append(messages, map[string]any{"role": role, "content": blocks})
	}
	for _, message := range history {
		switch message.Role {
		case "user":
			appendMessage("user", []any{map[string]any{"type": "text", "text": modelMessageText(message.Content)}})
		case "assistant":
			if len(message.AnthropicContent) > 0 {
				blocks := make([]any, 0, len(message.AnthropicContent))
				for _, block := range message.AnthropicContent {
					blocks = append(blocks, block)
				}
				appendMessage("assistant", blocks)
				continue
			}
			blocks := make([]any, 0, len(message.ToolCalls)+1)
			if content := modelMessageText(message.Content); content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": content})
			}
			for _, call := range message.ToolCalls {
				var input any
				if json.Unmarshal([]byte(call.Function.Arguments), &input) != nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input})
			}
			appendMessage("assistant", blocks)
		case "tool":
			appendMessage("user", []any{map[string]any{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": modelMessageText(message.Content)}})
		}
	}
	return messages
}

func anthropicTools() []map[string]any {
	tools := make([]map[string]any, 0, len(agentTools))
	for _, tool := range agentTools {
		function, _ := tool["function"].(map[string]any)
		tools = append(tools, map[string]any{
			"name":         function["name"],
			"description":  function["description"],
			"input_schema": function["parameters"],
		})
	}
	return tools
}

func modelMessageText(content any) string {
	if content == nil {
		return ""
	}
	if text, ok := content.(string); ok {
		return text
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return fmt.Sprint(content)
	}
	return string(encoded)
}
