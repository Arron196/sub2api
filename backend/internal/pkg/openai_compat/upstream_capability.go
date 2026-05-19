// Package openai_compat 提供 OpenAI 协议族在不同上游间的能力差异判定工具。
//
// 背景：sub2api 的 OpenAI APIKey 账号通过 base_url 接入多种第三方 OpenAI 兼容上游
// （DeepSeek、Kimi、GLM、Qwen 等）。这些上游对 /v1/responses 的支持不一致，网关
// 需要在保留旧行为的同时允许账号级显式覆盖。
//
// 本包提供基于"账号显式配置 + 探测标记"的能力判定，配合
// internal/service/openai_apikey_responses_probe.go 在创建/修改账号时一次性
// 探测并落标。
//
// 设计取舍：
//   - 不维护静态 host 白名单——避免新增厂商时必须改代码（讨论沉淀于
//     pensieve/short-term/knowledge/upstream-capability-detection-design-tradeoffs）
//   - 标记缺失时默认 true（即"走 Responses"），保持与重构前老代码完全一致的存量
//     账号行为；第三方自定义 base_url 在未探测时也先走 /v1/responses，错误后由
//     网关现有回退逻辑转为 /v1/chat/completions
package openai_compat

// AccountResponsesSupport 描述账号上游对 OpenAI Responses API 的有效支持状态。
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

// ResponsesSupportMode 描述账号级 Responses API 路由覆盖模式。
type ResponsesSupportMode string

const (
	// ResponsesSupportModeAuto 表示跟随自动探测结果。
	ResponsesSupportModeAuto ResponsesSupportMode = "auto"

	// ResponsesSupportModeForceResponses 强制使用 /v1/responses。
	ResponsesSupportModeForceResponses ResponsesSupportMode = "force_responses"

	// ResponsesSupportModeForceChatCompletions 强制使用 /v1/chat/completions。
	ResponsesSupportModeForceChatCompletions ResponsesSupportMode = "force_chat_completions"
)

const (
	// ExtraKeyResponsesMode 是 accounts.extra JSON 中存储手动覆盖模式的键名。
	// 值类型为 string：auto=跟随探测，force_responses=强制 Responses，
	// force_chat_completions=强制 Chat Completions。
	ExtraKeyResponsesMode = "openai_responses_mode"

	// ExtraKeyResponsesSupported 是 accounts.extra JSON 中存储自动探测结果的键名。
	// 值类型为 bool：true=支持、false=不支持、键缺失=未探测。
	ExtraKeyResponsesSupported = "openai_responses_supported"

	// ExtraKeyAPIKeyUpstreamMode 是 accounts.extra JSON 中存储 OpenAI APIKey
	// 上游接口模式的键名。该用户显式配置优先于兼容的 Responses 覆盖模式与探测结果。
	ExtraKeyAPIKeyUpstreamMode = "openai_apikey_upstream_mode"

	APIKeyUpstreamModeAuto            = "auto"
	APIKeyUpstreamModeResponses       = "responses"
	APIKeyUpstreamModeChatCompletions = "chat_completions"
)

// NormalizeResponsesSupportMode 归一化账号级 Responses API 路由覆盖模式。
// 缺失或非法值按 auto 处理，以保持存量行为。
func NormalizeResponsesSupportMode(mode string) ResponsesSupportMode {
	switch ResponsesSupportMode(mode) {
	case ResponsesSupportModeForceResponses:
		return ResponsesSupportModeForceResponses
	case ResponsesSupportModeForceChatCompletions:
		return ResponsesSupportModeForceChatCompletions
	default:
		return ResponsesSupportModeAuto
	}
}

// ResolveResponsesSupport 从账号的 extra map 中读取手动覆盖模式与探测标记。
//
// 标记缺失或类型不匹配时返回 ResponsesSupportUnknown——调用方应按
// "未探测=保留旧行为=走 Responses" 处理（参见 ShouldUseResponsesAPI）。
func ResolveResponsesSupport(extra map[string]any) AccountResponsesSupport {
	if extra == nil {
		return ResponsesSupportUnknown
	}
	if mode, ok := extra[ExtraKeyResponsesMode].(string); ok {
		switch NormalizeResponsesSupportMode(mode) {
		case ResponsesSupportModeForceResponses:
			return ResponsesSupportYes
		case ResponsesSupportModeForceChatCompletions:
			return ResponsesSupportNo
		}
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
// 优先级：
//  1. openai_apikey_upstream_mode 用户显式选择 responses/chat_completions
//  2. openai_responses_mode 兼容覆盖模式
//  3. openai_responses_supported 自动探测结果
//
// 自动模式下未探测账号默认返回 true（包括第三方自定义 base_url），保持旧行为，
// 由网关在 /v1/responses 返回不支持时回退到 /v1/chat/completions。
func ShouldUseResponsesAPI(extra map[string]any) bool {
	switch ResolveAPIKeyUpstreamMode(extra) {
	case APIKeyUpstreamModeResponses:
		return true
	case APIKeyUpstreamModeChatCompletions:
		return false
	}
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

// ShouldUseResponsesAPIForBaseURL 保留旧调用点的签名，但不再用 base_url 做
// 未探测账号的静态分流。自动模式 + 未探测时无论是否第三方自定义 base_url，都先走
// /v1/responses；如果上游报不支持，网关会回退到 /v1/chat/completions。
func ShouldUseResponsesAPIForBaseURL(extra map[string]any, baseURL string) bool {
	_ = baseURL
	return ShouldUseResponsesAPI(extra)
}
