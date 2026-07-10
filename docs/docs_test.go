package docs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSwaggerAccountSchemasMatchAPIContract(t *testing.T) {
	swagger := readSwaggerJSON(t)
	definitions := objectField(t, swagger, "definitions")

	create := schemaProperties(t, definitions, "handler.CreateAccountReq")
	assertHasOnlyProperties(t, create, "channel_id", "guild_id", "user_token")

	update := schemaProperties(t, definitions, "handler.UpdateAccountReq")
	assertHasProperties(t, update, "channel_id", "concurrent_limit", "guild_id", "is_disabled", "user_token")
	assertLacksProperties(t, update, "health", "is_healthy", "status")

	updateOperation := objectField(t, objectField(t, swagger, "paths"), "/api/v1/accounts/{id}")["put"]
	updateDescription := objectFieldValue(t, updateOperation, "description")
	if !strings.Contains(updateDescription, "At least one field must be provided") {
		t.Fatalf("update account description = %q, want one-field requirement", updateDescription)
	}

	view := schemaProperties(t, definitions, "handler.AccountView")
	assertHasProperties(t, view, "channel_id", "concurrent_limit", "current_jobs", "guild_id", "is_disabled", "is_healthy")
	assertLacksProperties(t, view, "health", "status", "user_token")
}

func TestSwaggerDocumentsDynamicAccountLifecycleRoutes(t *testing.T) {
	swagger := readSwaggerJSON(t)
	paths := objectField(t, swagger, "paths")

	accountPath := objectField(t, paths, "/api/v1/accounts/{id}")
	assertHasProperties(t, accountPath, "delete", "put")

	restartPath := objectField(t, paths, "/api/v1/accounts/{id}/restart")
	assertHasProperties(t, restartPath, "post")
}

func readSwaggerJSON(t *testing.T) map[string]any {
	t.Helper()

	data, err := os.ReadFile("swagger.json")
	if err != nil {
		t.Fatalf("read swagger.json: %v", err)
	}

	var swagger map[string]any
	if err := json.Unmarshal(data, &swagger); err != nil {
		t.Fatalf("unmarshal swagger.json: %v", err)
	}
	return swagger
}

func schemaProperties(t *testing.T, definitions map[string]any, name string) map[string]any {
	t.Helper()
	schema := objectField(t, definitions, name)
	return objectField(t, schema, "properties")
}

func objectField(t *testing.T, parent map[string]any, name string) map[string]any {
	t.Helper()

	value, ok := parent[name]
	if !ok {
		t.Fatalf("missing object field %q", name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("field %q has type %T, want object", name, value)
	}
	return object
}

func objectFieldValue(t *testing.T, value any, name string) string {
	t.Helper()

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value has type %T, want object", value)
	}
	field, ok := object[name]
	if !ok {
		t.Fatalf("missing string field %q", name)
	}
	text, ok := field.(string)
	if !ok {
		t.Fatalf("field %q has type %T, want string", name, field)
	}
	return text
}

func assertHasOnlyProperties(t *testing.T, properties map[string]any, names ...string) {
	t.Helper()
	assertHasProperties(t, properties, names...)
	if len(properties) != len(names) {
		t.Fatalf("properties = %#v, want only %#v", keys(properties), names)
	}
}

func assertHasProperties(t *testing.T, properties map[string]any, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties = %#v, missing %q", keys(properties), name)
		}
	}
}

func assertLacksProperties(t *testing.T, properties map[string]any, names ...string) {
	t.Helper()
	for _, name := range names {
		if _, ok := properties[name]; ok {
			t.Fatalf("properties = %#v, should not include %q", keys(properties), name)
		}
	}
}

func keys(m map[string]any) []string {
	result := make([]string, 0, len(m))
	for key := range m {
		result = append(result, key)
	}
	return result
}
