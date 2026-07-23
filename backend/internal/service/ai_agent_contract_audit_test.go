package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestValidateAgentBodyContractEnforcesSchemaConstraints(t *testing.T) {
	tests := []struct {
		name    string
		schema  map[string]any
		value   any
		wantErr bool
	}{
		{name: "numeric minimum", schema: map[string]any{"type": "number", "minimum": 1}, value: float64(0), wantErr: true},
		{name: "numeric maximum", schema: map[string]any{"type": "number", "maximum": 10}, value: float64(11), wantErr: true},
		{name: "fractional integer", schema: map[string]any{"type": "integer"}, value: 1.5, wantErr: true},
		{name: "integer", schema: map[string]any{"type": "integer", "minimum": 1}, value: float64(2)},
		{name: "array minimum", schema: map[string]any{"type": "array", "minimum": 2, "items": map[string]any{"type": "string"}}, value: []any{"one"}, wantErr: true},
		{name: "array maximum", schema: map[string]any{"type": "array", "maximum": 1, "items": map[string]any{"type": "string"}}, value: []any{"one", "two"}, wantErr: true},
		{name: "string minimum", schema: map[string]any{"type": "string", "minimum": 2}, value: "a", wantErr: true},
		{name: "string maximum", schema: map[string]any{"type": "string", "maximum": 2}, value: "abc", wantErr: true},
		{name: "invalid date time", schema: map[string]any{"type": "string", "format": "date-time"}, value: "2026-99-99", wantErr: true},
		{name: "valid date time", schema: map[string]any{"type": "string", "format": "date-time"}, value: "2026-07-23T11:00:00Z"},
		{name: "unknown object field", schema: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}}}, value: map[string]any{"unknown": true}, wantErr: true},
		{name: "typed additional property", schema: map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, value: map[string]any{"key": float64(1)}, wantErr: true},
		{name: "valid typed additional property", schema: map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}, value: map[string]any{"key": "value"}},
		{name: "untyped additional property", schema: map[string]any{"type": "object", "additionalProperties": map[string]any{}}, value: map[string]any{"key": []any{float64(1)}}},
		{name: "nullable field", schema: map[string]any{"type": "number", "minimum": 1}, value: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateAgentBodyContract(test.schema, test.value, "body")
			if (err != nil) != test.wantErr {
				t.Fatalf("validateAgentBodyContract() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateAgentOperationSemanticsCoversAuditedConditionalRules(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   map[string]any
	}{
		{name: "batch users need targets", method: "POST", path: "/admin/users/batch-concurrency", body: map[string]any{"all": false}},
		{name: "affiliate rate required", method: "POST", path: "/admin/affiliates/users/batch-rate", body: map[string]any{"clear": false}},
		{name: "subscription reset needs true window", method: "POST", path: "/admin/subscriptions/12/reset-quota", body: map[string]any{"daily": false, "weekly": false, "monthly": false}},
		{name: "redeem expiry conflict", method: "POST", path: "/admin/redeem-codes/generate", body: map[string]any{"expires_at": "2030-01-01T00:00:00Z", "expires_in_days": float64(30)}},
		{name: "subscription redeem validity", method: "POST", path: "/admin/redeem-codes/create-and-redeem", body: map[string]any{"type": "subscription", "group_id": float64(1), "validity_days": float64(0)}},
		{name: "channel pricing target", method: "PUT", path: "/admin/channels/3", body: map[string]any{"account_stats_pricing_rules": []any{map[string]any{"pricing": []any{map[string]any{"billing_mode": "token"}}}}}},
		{name: "group hold below discount", method: "PUT", path: "/admin/groups/4", body: map[string]any{"batch_image_discount_multiplier": 0.8, "batch_image_hold_multiplier": 0.7}},
		{name: "credential discriminator", method: "POST", path: "/admin/accounts/batch-update-credentials", body: map[string]any{"field": "intercept_warmup_requests", "value": "true"}},
		{name: "cleanup reversed range", method: "POST", path: "/admin/usage/cleanup-tasks", body: map[string]any{"start_date": "2026-02-02", "end_date": "2026-02-01"}},
		{name: "invalid plan currency", method: "POST", path: "/admin/payment/plans", body: map[string]any{"currency": "US"}},
		{name: "recharge precision", method: "PUT", path: "/admin/payment/config", body: map[string]any{"recharge_fee_rate": 1.234}},
		{name: "rate threshold range", method: "POST", path: "/admin/ops/alert-rules", body: map[string]any{"metric_type": "error_rate", "threshold": 101.0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateAgentOperationSemantics(test.method, test.path, test.body); err == nil {
				t.Fatal("validateAgentOperationSemantics() unexpectedly accepted invalid payload")
			}
		})
	}
}

func TestMergeAgentSingletonPutBodyPreservesCurrentNonSensitiveFields(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"level":   map[string]any{"type": "string"},
		"caller":  map[string]any{"type": "boolean"},
		"api_key": map[string]any{"type": "string"},
		"count":   map[string]any{"type": "integer"},
	}}
	before := map[string]any{"level": "warn", "caller": true, "api_key": "secret", "count": "invalid", "server_only": "ignored"}
	merged := mergeAgentSingletonPutBody(schema, before, map[string]any{"level": "error"})
	if merged["level"] != "error" || merged["caller"] != true {
		t.Fatalf("singleton merge lost requested or current values: %#v", merged)
	}
	for _, forbidden := range []string{"api_key", "count", "server_only"} {
		if _, exists := merged[forbidden]; exists {
			t.Fatalf("singleton merge retained forbidden current field %s: %#v", forbidden, merged)
		}
	}
	withUnknown := mergeAgentSingletonPutBody(schema, before, map[string]any{"unknown": true})
	if err := validateAgentBodyContract(schema, withUnknown, "body"); err == nil {
		t.Fatalf("singleton merge silently dropped an explicit unknown field: %#v", withUnknown)
	}
}

func TestAIAgentWriteCatalogHasCompleteBodyClassification(t *testing.T) {
	service, err := NewAIAgentService(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAIAgentService() error = %v", err)
	}
	writes, contracted, verifiedBodyless := 0, 0, 0
	for _, operation := range service.catalog {
		if operation.Method == "GET" {
			continue
		}
		writes++
		if len(operation.BodySchema) > 0 {
			contracted++
			continue
		}
		verifiedBodyless++
		if len(operation.BodyExample) > 0 {
			t.Errorf("bodyless operation %s still exposes a body example", operation.Key)
		}
	}
	if writes != 220 || contracted != 157 || verifiedBodyless != 63 {
		t.Fatalf("write classification = writes:%d contracts:%d bodyless:%d, want 220/157/63", writes, contracted, verifiedBodyless)
	}
}

func TestAIAgentCatalogBodyExamplesMatchContracts(t *testing.T) {
	service, err := NewAIAgentService(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAIAgentService() error = %v", err)
	}
	var violations []string
	for _, operation := range service.catalog {
		if len(operation.BodySchema) == 0 || len(operation.BodyExample) == 0 {
			continue
		}
		if err := validateAgentBodyContract(operation.BodySchema, operation.BodyExample, "body"); err != nil {
			violations = append(violations, operation.Key+": "+err.Error())
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("Agent body examples violate their contracts:\n%s", strings.Join(violations, "\n"))
	}
}

func TestAIAgentContractsContainNoOpaqueConcreteSchemas(t *testing.T) {
	var contracts map[string]struct {
		BodySchema map[string]any `json:"body_schema"`
	}
	if err := json.Unmarshal(agentContractsJSON, &contracts); err != nil {
		t.Fatalf("decode Agent contracts: %v", err)
	}

	allowedUntyped := map[string]bool{
		"POST:/admin/accounts/batch-update-credentials:body.value": true,
	}
	var violations []string
	for endpoint, contract := range contracts {
		auditAgentContractSchema(endpoint, "body", contract.BodySchema, allowedUntyped, &violations)
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("Agent contracts contain opaque or invalid schemas:\n%s", strings.Join(violations, "\n"))
	}
}

func TestAIAgentContractsPreserveCustomDecoderAndDomainAliasShapes(t *testing.T) {
	var contracts map[string]struct {
		BodySchema map[string]any `json:"body_schema"`
	}
	if err := json.Unmarshal(agentContractsJSON, &contracts); err != nil {
		t.Fatalf("decode Agent contracts: %v", err)
	}

	expected := map[string]string{
		"POST:/admin/groups:daily_limit_usd":                                     "number",
		"POST:/admin/groups:weekly_limit_usd":                                    "number",
		"POST:/admin/groups:monthly_limit_usd":                                   "number",
		"PUT:/admin/groups/:id:daily_limit_usd":                                  "number",
		"PUT:/admin/groups/:id:weekly_limit_usd":                                 "number",
		"PUT:/admin/groups/:id:monthly_limit_usd":                                "number",
		"POST:/admin/redeem-codes/batch-update:fields.expires_at":                "string",
		"POST:/admin/redeem-codes/batch-update:fields.group_id":                  "integer",
		"POST:/admin/announcements:targeting.any_of[].all_of[].group_ids":        "array",
		"PUT:/admin/announcements/:id:targeting.any_of[].all_of[].group_ids":     "array",
		"POST:/admin/groups:messages_dispatch_model_config.exact_model_mappings": "object",
		"PUT:/admin/groups/:id:models_list_config.models":                        "array",
		"POST:/admin/groups:reasoning_effort_mappings[].from":                    "string",
		"PUT:/admin/groups/:id:reasoning_effort_mappings[].to":                   "string",
	}
	for identity, expectedType := range expected {
		separator := strings.LastIndex(identity, ":")
		endpoint, fieldPath := identity[:separator], identity[separator+1:]
		contract, exists := contracts[endpoint]
		if !exists {
			t.Errorf("missing contract %s", endpoint)
			continue
		}
		schema := agentContractSchemaAt(contract.BodySchema, fieldPath)
		if schema["type"] != expectedType {
			t.Errorf("%s %s type = %v, want %s", endpoint, fieldPath, schema["type"], expectedType)
		}
	}
}

func TestAIAgentContractsExcludeKnownServerManagedFields(t *testing.T) {
	var contracts map[string]struct {
		BodySchema map[string]any `json:"body_schema"`
	}
	if err := json.Unmarshal(agentContractsJSON, &contracts); err != nil {
		t.Fatalf("decode Agent contracts: %v", err)
	}

	serverManaged := map[string][]string{
		"POST:/admin/ops/alert-rules":      {"id", "created_at", "updated_at", "last_triggered_at"},
		"PUT:/admin/ops/alert-rules/:id":   {"id", "created_at", "updated_at", "last_triggered_at"},
		"PUT:/admin/ops/runtime/logging":   {"source", "updated_at", "updated_by_user_id"},
		"PUT:/admin/ops/advanced-settings": {"ignore_invalid_api_key_errors"},
	}
	for endpoint, fields := range serverManaged {
		properties, _ := contracts[endpoint].BodySchema["properties"].(map[string]any)
		for _, field := range fields {
			if _, exists := properties[field]; exists {
				t.Errorf("%s exposes server-managed field %s", endpoint, field)
			}
		}
	}
	providerSchema := agentContractSchemaAt(contracts["PUT:/admin/settings/web-search-emulation"].BodySchema, "providers[]")
	providerProperties, _ := providerSchema["properties"].(map[string]any)
	for _, field := range []string{"api_key_configured", "quota_used"} {
		if _, exists := providerProperties[field]; exists {
			t.Errorf("web search provider contract exposes server-managed field %s", field)
		}
	}
}

func agentContractSchemaAt(schema map[string]any, path string) map[string]any {
	for _, component := range strings.Split(path, ".") {
		array := strings.HasSuffix(component, "[]")
		component = strings.TrimSuffix(component, "[]")
		properties, _ := schema["properties"].(map[string]any)
		schema, _ = properties[component].(map[string]any)
		if array {
			schema, _ = schema["items"].(map[string]any)
		}
	}
	return schema
}

func auditAgentContractSchema(endpoint, path string, schema map[string]any, allowedUntyped map[string]bool, violations *[]string) {
	if len(schema) == 0 {
		if path == "additionalProperties" || strings.HasSuffix(path, ".additionalProperties") || allowedUntyped[endpoint+":"+path] {
			return
		}
		*violations = append(*violations, fmt.Sprintf("%s %s has no type", endpoint, path))
		return
	}

	allowedKeywords := map[string]bool{
		"type": true, "properties": true, "additionalProperties": true, "items": true,
		"required": true, "required_any": true, "enum": true, "minimum": true, "maximum": true,
		"exclusiveMinimum": true, "exclusiveMaximum": true, "format": true, "default": true,
	}
	for keyword := range schema {
		if !allowedKeywords[keyword] {
			*violations = append(*violations, fmt.Sprintf("%s %s uses unenforced keyword %s", endpoint, path, keyword))
		}
	}
	if format, _ := schema["format"].(string); format != "" {
		supportedFormats := map[string]bool{"date-time": true, "date": true, "email": true, "http-url": true, "semver": true}
		if !supportedFormats[format] {
			*violations = append(*violations, fmt.Sprintf("%s %s uses unsupported format %s", endpoint, path, format))
		}
	}

	schemaType, _ := schema["type"].(string)
	switch schemaType {
	case "object":
		properties, hasProperties := schema["properties"].(map[string]any)
		additional, hasAdditional := schema["additionalProperties"].(map[string]any)
		if (!hasProperties || len(properties) == 0) && !hasAdditional {
			*violations = append(*violations, fmt.Sprintf("%s %s is an opaque object", endpoint, path))
			return
		}
		for field, raw := range properties {
			fieldSchema, ok := raw.(map[string]any)
			if !ok {
				*violations = append(*violations, fmt.Sprintf("%s %s.%s is not a schema", endpoint, path, field))
				continue
			}
			auditAgentContractSchema(endpoint, path+"."+field, fieldSchema, allowedUntyped, violations)
		}
		if hasAdditional {
			auditAgentContractSchema(endpoint, path+".additionalProperties", additional, allowedUntyped, violations)
		}
	case "array":
		items, ok := schema["items"].(map[string]any)
		if !ok {
			*violations = append(*violations, fmt.Sprintf("%s %s is an array without item schema", endpoint, path))
			return
		}
		auditAgentContractSchema(endpoint, path+"[]", items, allowedUntyped, violations)
	case "string", "boolean", "integer", "number":
	case "":
		if !allowedUntyped[endpoint+":"+path] {
			*violations = append(*violations, fmt.Sprintf("%s %s has no type", endpoint, path))
		}
	default:
		*violations = append(*violations, fmt.Sprintf("%s %s uses unsupported type %q", endpoint, path, schemaType))
	}
}
