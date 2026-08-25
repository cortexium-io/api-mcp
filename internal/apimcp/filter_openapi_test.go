package apimcp

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

var baseTestConfig = Config{
	Name:             "test",
	Version:          "0.1.0",
	Spec:             "unused",
	Auth:             ResolvedAuthConfig{Type: "none"},
	Headers:          map[string]string{},
	Tools:            ToolFilterConfig{},
	MaxResponseChars: 20_000,
}

func TestCollectOperationsCreatesStableToolNames(t *testing.T) {
	doc := &openapi3.T{
		OpenAPI: "3.0.3",
		Info:    &openapi3.Info{Title: "Test", Version: "1.0.0"},
		Servers: openapi3.Servers{{URL: "https://api.example.com"}},
		Paths:   openapi3.NewPaths(),
	}
	pathItem := &openapi3.PathItem{}
	pathItem.SetOperation("GET", &openapi3.Operation{
		OperationID: "getPetById",
		Summary:     "Get pet",
		Parameters: openapi3.Parameters{
			{Value: &openapi3.Parameter{
				Name:     "petId",
				In:       "path",
				Required: true,
				Schema:   &openapi3.SchemaRef{Value: openapi3.NewIntegerSchema()},
			}},
		},
	})
	pathItem.SetOperation("POST", &openapi3.Operation{Summary: "Update pet"})
	doc.Paths.Set("/pets/{petId}", pathItem)

	operations := CollectOperations(doc, baseTestConfig)
	if got, want := len(operations), 2; got != want {
		t.Fatalf("operation count = %d, want %d", got, want)
	}
	if got, want := operations[0].ToolName, "get_pet_by_id"; got != want {
		t.Fatalf("first tool = %q, want %q", got, want)
	}
	if got, want := operations[1].ToolName, "post_pets_pet_id"; got != want {
		t.Fatalf("second tool = %q, want %q", got, want)
	}
	if !operations[0].Parameters[0].Required {
		t.Fatal("path parameter should be required")
	}
}

func TestFilterIncludeAndExcludePatternsMatchOperationMetadata(t *testing.T) {
	operation := OperationDescriptor{
		ToolName:    "get_pet_by_id",
		OperationID: "getPetById",
		Method:      "get",
		Path:        "/pet/{petId}",
		ServerURL:   "https://api.example.com",
		Title:       "Get pet",
		Description: "Get pet",
		Tags:        []string{"pets"},
	}

	if !MatchesPattern("get_*", operation.ToolName) {
		t.Fatal("expected wildcard match")
	}
	if !ShouldExposeOperation(operation, ToolFilterConfig{Include: []string{"tag:pets"}}) {
		t.Fatal("expected tag include to expose operation")
	}
	if ShouldExposeOperation(operation, ToolFilterConfig{Include: []string{"tag:pets"}, Exclude: []string{"GET /pet/{petId}"}}) {
		t.Fatal("expected exclude to win")
	}
}

func TestFilterDenyDefaultRequiresAnExplicitInclude(t *testing.T) {
	operation := OperationDescriptor{ToolName: "get_pet_by_id", Method: "get", Path: "/pet/{petId}"}

	if !ShouldExposeOperation(operation, ToolFilterConfig{}) {
		t.Fatal("legacy empty filter should continue to allow operations")
	}
	if ShouldExposeOperation(operation, ToolFilterConfig{DefaultPolicy: "deny"}) {
		t.Fatal("deny-default filter should reject an operation without an include")
	}
	if !ShouldExposeOperation(operation, ToolFilterConfig{DefaultPolicy: "deny", Include: []string{"GET *"}}) {
		t.Fatal("deny-default filter should allow an explicitly included operation")
	}
}

func TestLoadOperationsHandlesGenericFixture(t *testing.T) {
	cfg := baseTestConfig
	cfg.Spec = filepath.Join("..", "..", "testdata", "openapi.yaml")

	operations, err := LoadOperations(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(operations), 7; got != want {
		t.Fatalf("operation count = %d, want %d", got, want)
	}

	byName := operationsByToolName(operations)
	if got, want := byName["get_widget"].ServerURL, "https://api.example.com/v1"; got != want {
		t.Fatalf("server URL = %q, want %q", got, want)
	}
	if got, want := byName["list_widgets"].Path, "/widgets"; got != want {
		t.Fatalf("list widgets path = %q, want %q", got, want)
	}
	if !hasRequiredParameter(byName["list_widgets"], "query", "ownerId") {
		t.Fatal("list_widgets should require query.ownerId")
	}
	triggerAction := byName["trigger_widget_action"]
	if triggerAction.Method != "post" || triggerAction.Path != "/widgets/{id}/actions" {
		t.Fatalf("unexpected trigger action operation: %+v", triggerAction)
	}
	if triggerAction.RequestBody == nil || !slices.Contains(triggerAction.RequestBody.ContentTypes, "application/json") {
		t.Fatal("expected trigger_widget_action to accept application/json")
	}
	if !strings.Contains(triggerAction.Description, "ask the user for explicit confirmation") {
		t.Fatalf("mutating operation description is missing confirmation warning: %q", triggerAction.Description)
	}
	if strings.Contains(byName["list_widgets"].Description, "ask the user for explicit confirmation") {
		t.Fatalf("read-only operation should not include confirmation warning: %q", byName["list_widgets"].Description)
	}
}

func TestManagedExampleConfigLoadsGenericOperations(t *testing.T) {
	cfg, err := LoadConfigFile(filepath.Join("..", "..", "examples", "managed.config.example.json"), mapLookup(map[string]string{
		"EXAMPLE_API_KEY": "test-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	operations, err := LoadOperations(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(operations), 7; got != want {
		t.Fatalf("operation count = %d, want %d", got, want)
	}
	if cfg.ToolPolicy == nil || !slices.Equal(cfg.ToolPolicy.DefaultFeatures, []string{"read-only"}) {
		t.Fatalf("unexpected managed tool policy: %+v", cfg.ToolPolicy)
	}
	byName := operationsByToolName(operations)
	for _, name := range []string{"get_current_account", "list_widgets", "create_widget"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("expected operation %q", name)
		}
	}
}

func operationsByToolName(operations []OperationDescriptor) map[string]OperationDescriptor {
	result := map[string]OperationDescriptor{}
	for _, operation := range operations {
		result[operation.ToolName] = operation
	}
	return result
}

func hasRequiredParameter(operation OperationDescriptor, location, name string) bool {
	for _, parameter := range operation.Parameters {
		if parameter.In == location && parameter.Name == name && parameter.Required {
			return true
		}
	}
	return false
}
