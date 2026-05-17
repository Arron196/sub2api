// Package openai_compat 提供 OpenAI 协议族在不同上游间的能力差异判定工具。
//
// 背景：sub2api 的 OpenAI APIKey 账号通过 base_url 接入多种第三方 OpenAI 兼容上游
// （DeepSeek、Kimi、GLM、Qwen 等）。这些上游普遍只支持 /v1/chat/completions，
// 不存在 /v1/responses 端点。但网关历史代码无差别走 CC→Responses 转换并打到
// /v1/responses，导致兼容上游 404。
//
// 本包提供基于"账号探测标记"的能力判定，配合
// internal/service/openai_apikey_responses_probe.go 在创建/修改账号时一次性
// 探测并落标。
//
// 设计取舍：
//   - 不维护静态 host 白名单——避免新增厂商时必须改代码（讨论沉淀于
//     pensieve/short-term/knowledge/upstream-capability-detection-design-tradeoffs）
//   - 标记缺失时默认 true（即"走 Responses"），保持与重构前老代码完全一致的存量
//     账号行为（"现状即证据"原则；详见
//     pensieve/short-term/maxims/preserve-existing-runtime-behavior-when-replacing-logic-in-stateful-systems）
package openai_compat

import (
	"net/url"
	"strings"
)

// AccountResponsesSupport 描述账号上游对 OpenAI Responses API 的支持状态。
//
// 仅用于 platform=openai + type=apikey 的账号；其他账号类型不应调用本包判定。
type AccountResponsesSupport int

const (
	// ResponsesSupportUnknown 表示账号尚未完成能力探测（extra 字段缺失）。
	// 上游路由层应按"现状即证据"原则默认走 Responses，保持与重构前一致。
	ResponsesSupportUnknown AccountResponsesSupport = iota

	// ResponsesSupportYes 探测确认上游支持 /v1/responses。
	ResponsesSupportYes

	// ResponsesSupportNo 探测确认上游不支持 /v1/responses，应走
	// /v1/chat/completions 直转路径。
	ResponsesSupportNo
)

const (
	// ExtraKeyResponsesSupported 是 accounts.extra JSON 中存储探测结果的键名。
	// 值类型为 bool：true=支持、false=不支持、键缺失=未探测。
	ExtraKeyResponsesSupported = "openai_responses_supported"

	// ExtraKeyAPIKeyUpstreamMode 是 accounts.extra JSON 中存储 OpenAI APIKey
	// 上游接口模式的键名。该用户显式配置优先于自动探测结果。
	ExtraKeyAPIKeyUpstreamMode = "openai_apikey_upstream_mode"

	APIKeyUpstreamModeAuto            = "auto"
	APIKeyUpstreamModeResponses       = "responses"
	APIKeyUpstreamModeChatCompletions = "chat_completions"
)

// ResolveResponsesSupport 从账号的 extra map 中读取探测标记。
//
// 标记缺失或类型不匹配时返回 ResponsesSupportUnknown——调用方应按
// "未探测=保留旧行为=走 Responses" 处理（参见 ShouldUseResponsesAPI）。
func ResolveResponsesSupport(extra map[string]any) AccountResponsesSupport {
	if extra == nil {
		return ResponsesSupportUnknown
	}
	v, ok := extra[ExtraKeyResponsesSupported]
	if !ok {
		return ResponsesSupportUnknown
	}
	supported, ok := v.(bool)
	if !ok {
		return ResponsesSupportUnknown
	}
	if supported {
		return ResponsesSupportYes
	}
	return ResponsesSupportNo
}

// ShouldUseResponsesAPI 判断 OpenAI APIKey 账号的入站 /v1/chat/completions 请求
// 是否应走"CC→Responses 转换 + 上游 /v1/responses"路径。
//
// 返回 true 的两种情况：
//  1. 账号已探测确认支持 Responses
//  2. 账号未探测（标记缺失）——按"现状即证据"原则保留旧行为
//
// 仅当账号已探测且确认不支持时返回 false，此时调用方应走 CC 直转路径
// （详见 internal/service/openai_gateway_chat_completions_raw.go）。
func ShouldUseResponsesAPI(extra map[string]any) bool {
	return ResolveResponsesSupport(extra) != ResponsesSupportNo
}

// ResolveAPIKeyUpstreamMode 从账号 extra map 中读取用户配置的 OpenAI APIKey
// 上游接口模式。缺失或非法值按 auto 处理。
func ResolveAPIKeyUpstreamMode(extra map[string]any) string {
	if extra == nil {
		return APIKeyUpstreamModeAuto
	}
	mode, ok := extra[ExtraKeyAPIKeyUpstreamMode].(string)
	if !ok {
		return APIKeyUpstreamModeAuto
	}
	switch mode {
	case APIKeyUpstreamModeResponses, APIKeyUpstreamModeChatCompletions:
		return mode
	default:
		return APIKeyUpstreamModeAuto
	}
}

// ShouldUseResponsesAPIForBaseURL 判断 OpenAI APIKey 账号的入站
// /v1/chat/completions 请求是否应走 Responses 路径。
//
// 与 ShouldUseResponsesAPI 的区别：未探测账号会结合显式 base_url 判定。
//   - 未配置 base_url：视为官方 OpenAI 默认上游，继续走 Responses，保持旧行为
//   - 显式配置 api.openai.com：继续走 Responses
//   - 显式配置第三方兼容上游：先走 /v1/chat/completions 直转，避免异步探测
//     写回前的首请求打到不存在的 /v1/responses
//
// 用户显式选择 responses/chat_completions 时优先于探测标记；auto 模式才使用
// 探测结果与 base_url 自动判断。
func ShouldUseResponsesAPIForBaseURL(extra map[string]any, baseURL string) bool {
	switch ResolveAPIKeyUpstreamMode(extra) {
	case APIKeyUpstreamModeResponses:
		return true
	case APIKeyUpstreamModeChatCompletions:
		return false
	}

	switch ResolveResponsesSupport(extra) {
	case ResponsesSupportYes:
		return true
	case ResponsesSupportNo:
		return false
	default:
		return isOfficialOpenAIBaseURL(baseURL)
	}
}

func isOfficialOpenAIBaseURL(baseURL string) bool {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return true
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		parsed, err = url.Parse("https://" + baseURL)
		if err != nil {
			return false
		}
	}

	return strings.EqualFold(parsed.Hostname(), "api.openai.com")
}
