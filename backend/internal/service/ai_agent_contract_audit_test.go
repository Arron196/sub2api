package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

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
		"POST:/admin/ops/alert-rules":    {"id", "created_at", "updated_at", "last_triggered_at"},
		"PUT:/admin/ops/alert-rules/:id": {"id", "created_at", "updated_at", "last_triggered_at"},
		"PUT:/admin/ops/runtime/logging": {"source", "updated_at", "updated_by_user_id"},
	}
	for endpoint, fields := range serverManaged {
		properties, _ := contracts[endpoint].BodySchema["properties"].(map[string]any)
		for _, field := range fields {
			if _, exists := properties[field]; exists {
				t.Errorf("%s exposes server-managed field %s", endpoint, field)
			}
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
