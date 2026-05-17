package openai_compat

import "testing"

func TestResolveResponsesSupport(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  AccountResponsesSupport
	}{
		{"nil extra", nil, ResponsesSupportUnknown},
		{"empty extra", map[string]any{}, ResponsesSupportUnknown},
		{"key missing", map[string]any{"other": "value"}, ResponsesSupportUnknown},
		{"value true", map[string]any{ExtraKeyResponsesSupported: true}, ResponsesSupportYes},
		{"value false", map[string]any{ExtraKeyResponsesSupported: false}, ResponsesSupportNo},
		{"value wrong type string", map[string]any{ExtraKeyResponsesSupported: "true"}, ResponsesSupportUnknown},
		{"value wrong type number", map[string]any{ExtraKeyResponsesSupported: 1}, ResponsesSupportUnknown},
		{"value nil", map[string]any{ExtraKeyResponsesSupported: nil}, ResponsesSupportUnknown},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveResponsesSupport(tc.extra)
			if got != tc.want {
				t.Errorf("ResolveResponsesSupport(%v) = %v, want %v", tc.extra, got, tc.want)
			}
		})
	}
}

func TestShouldUseResponsesAPI(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  bool
	}{
		// 关键不变量：未探测必须返回 true（保留旧行为）
		{"unknown defaults to true (preserve old behavior)", nil, true},
		{"unknown empty defaults to true", map[string]any{}, true},
		{"unknown wrong type defaults to true", map[string]any{ExtraKeyResponsesSupported: "yes"}, true},

		// 已探测：标记决定
		{"explicitly supported", map[string]any{ExtraKeyResponsesSupported: true}, true},
		{"explicitly unsupported", map[string]any{ExtraKeyResponsesSupported: false}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseResponsesAPI(tc.extra)
			if got != tc.want {
				t.Errorf("ShouldUseResponsesAPI(%v) = %v, want %v", tc.extra, got, tc.want)
			}
		})
	}
}

func TestResolveAPIKeyUpstreamMode(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"nil extra", nil, APIKeyUpstreamModeAuto},
		{"empty extra", map[string]any{}, APIKeyUpstreamModeAuto},
		{"explicit auto", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeAuto}, APIKeyUpstreamModeAuto},
		{"explicit responses", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeResponses}, APIKeyUpstreamModeResponses},
		{"explicit chat completions", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeChatCompletions}, APIKeyUpstreamModeChatCompletions},
		{"unknown string", map[string]any{ExtraKeyAPIKeyUpstreamMode: "legacy"}, APIKeyUpstreamModeAuto},
		{"wrong type", map[string]any{ExtraKeyAPIKeyUpstreamMode: true}, APIKeyUpstreamModeAuto},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveAPIKeyUpstreamMode(tc.extra)
			if got != tc.want {
				t.Errorf("ResolveAPIKeyUpstreamMode(%v) = %q, want %q", tc.extra, got, tc.want)
			}
		})
	}
}

func TestShouldUseResponsesAPIForBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		extra   map[string]any
		baseURL string
		want    bool
	}{
		{"forced responses overrides unsupported probe and custom base URL", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeResponses, ExtraKeyResponsesSupported: false}, "https://api.deepseek.com", true},
		{"forced chat completions overrides supported probe and official base URL", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeChatCompletions, ExtraKeyResponsesSupported: true}, "https://api.openai.com", false},
		{"auto mode follows unsupported probe", map[string]any{ExtraKeyAPIKeyUpstreamMode: APIKeyUpstreamModeAuto, ExtraKeyResponsesSupported: false}, "https://api.openai.com", false},
		{"invalid mode falls back to auto base URL behavior", map[string]any{ExtraKeyAPIKeyUpstreamMode: "legacy"}, "https://api.deepseek.com", false},

		{"explicitly supported ignores custom base URL", map[string]any{ExtraKeyResponsesSupported: true}, "https://api.deepseek.com", true},
		{"explicitly unsupported ignores official base URL", map[string]any{ExtraKeyResponsesSupported: false}, "https://api.openai.com", false},

		{"unknown empty base URL preserves default OpenAI behavior", nil, "", true},
		{"unknown official bare domain", nil, "https://api.openai.com", true},
		{"unknown official trailing slash", nil, "https://api.openai.com/", true},
		{"unknown official v1 path", nil, "https://api.openai.com/v1", true},
		{"unknown official responses path", nil, "https://api.openai.com/v1/responses", true},
		{"unknown official no scheme", nil, "api.openai.com", true},
		{"unknown official mixed case and port", nil, "HTTPS://API.OPENAI.COM:443/v1", true},

		{"unknown third party bare domain uses raw chat completions", nil, "https://api.deepseek.com", false},
		{"unknown third party path prefix uses raw chat completions", nil, "https://api.gptgod.online/api", false},
		{"unknown third party no scheme uses raw chat completions", nil, "api.deepseek.com", false},
		{"unknown malformed explicit base URL uses raw chat completions", nil, "://bad", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldUseResponsesAPIForBaseURL(tc.extra, tc.baseURL)
			if got != tc.want {
				t.Errorf("ShouldUseResponsesAPIForBaseURL(%v, %q) = %v, want %v", tc.extra, tc.baseURL, got, tc.want)
			}
		})
	}
}
