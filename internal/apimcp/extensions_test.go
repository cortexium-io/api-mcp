package apimcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtensionBatchCancellationDoesNotReadCredentialOrSendHTTP(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := &fakeKeychainStore{values: map[string][]byte{}}
	prompter := &fakeCredentialPrompter{value: []byte("secret")}
	confirmer := &fakeActionConfirmer{err: ErrActionConfirmationCanceled}
	runtime := extensionRuntimeForTest(t, server.URL, store, prompter, confirmer)
	auth := ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
		CredentialMissing: true,
	}
	runtime.config.Auth = auth
	runtime.credentials = newCredentialManager(runtime.config.Name, auth, store, prompter)

	_, err := runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Delete duplicate bank lines",
		Items: []ExtensionBatchItem{{
			ID:        "line-1",
			Operation: "delete_widget",
			Input:     ToolInput{Path: map[string]any{"id": "line-1"}},
		}},
		StopOnError: true,
	})
	if err == nil || !strings.Contains(err.Error(), "confirmation was canceled") {
		t.Fatalf("canceled batch error = %v", err)
	}
	if requestCount != 0 || store.getCalls != 0 || store.setCalls != 0 || prompter.calls != 0 {
		t.Fatalf("canceled batch crossed protected boundary: requests=%d keychainGets=%d keychainSets=%d prompts=%d", requestCount, store.getCalls, store.setCalls, prompter.calls)
	}
	if got, want := len(confirmer.confirmations), 1; got != want {
		t.Fatalf("confirmations = %d, want %d", got, want)
	}
}

func TestExtensionBatchUsesOneConfirmationForMultipleWrites(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	confirmer := &fakeActionConfirmer{}
	runtime := extensionRuntimeForTest(t, server.URL, &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, confirmer)
	results, err := runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Delete duplicate bank lines",
		Items: []ExtensionBatchItem{
			{ID: "line-1", Operation: "delete_widget", Input: ToolInput{Path: map[string]any{"id": "line-1"}}},
			{ID: "line-2", Operation: "delete_widget", Input: ToolInput{Path: map[string]any{"id": "line-2"}}},
		},
		StopOnError: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(confirmer.confirmations), 1; got != want {
		t.Fatalf("confirmations = %d, want %d", got, want)
	}
	if got, want := requestCount, 2; got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}
	if got, want := len(results), 2; got != want {
		t.Fatalf("results = %d, want %d", got, want)
	}
	for _, result := range results {
		if result.Error != "" || result.Response == nil || !result.Response.OK {
			t.Fatalf("unexpected batch result: %+v", result)
		}
	}
}

func TestExtensionBatchRejectsReadOperations(t *testing.T) {
	runtime := extensionRuntimeForTest(t, "https://api.example.test", &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, &fakeActionConfirmer{})
	_, err := runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Invalid batch",
		Items: []ExtensionBatchItem{{
			ID:        "read-1",
			Operation: "get_current_account",
		}},
	})
	if err == nil {
		t.Fatal("read operation was accepted in a write batch")
	}
}

func TestExtensionBatchValidatesEveryItemBeforeConfirmation(t *testing.T) {
	confirmer := &fakeActionConfirmer{}
	runtime := extensionRuntimeForTest(t, "https://api.example.test", &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, confirmer)
	_, err := runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Invalid batch",
		Items: []ExtensionBatchItem{
			{ID: "valid", Operation: "delete_widget", Input: ToolInput{Path: map[string]any{"id": "one"}}},
			{ID: "invalid", Operation: "delete_widget", Input: ToolInput{}},
		},
	})
	if err == nil {
		t.Fatal("invalid batch was accepted")
	}
	if len(confirmer.confirmations) != 0 {
		t.Fatalf("invalid batch requested confirmation: %+v", confirmer.confirmations)
	}
}

func TestExtensionBatchEnforcesCurrentToolPolicy(t *testing.T) {
	confirmer := &fakeActionConfirmer{}
	runtime := extensionRuntimeForTest(t, "https://api.example.test", &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, confirmer)
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	operations := make([]OperationDescriptor, 0, len(runtime.operations))
	for _, operation := range runtime.operations {
		operations = append(operations, operation)
	}
	if err := writeJSONFileAtomically(catalogPath, ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{{
			ID:          "record-writes",
			Title:       "Record writes",
			Description: "Write test records.",
			Risk:        "write",
			Endpoints:   []string{"delete_widget"},
		}},
	}, true); err != nil {
		t.Fatal(err)
	}
	manager, err := newToolPolicyManager(ResolvedToolPolicyConfig{
		UserConfigPath:  filepath.Join(directory, "policy.json"),
		CatalogPath:     catalogPath,
		DefaultFeatures: []string{"record-writes"},
	}, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.policy = manager
	if _, err := runtime.validateEnabledOperation("", "delete_widget", ToolInput{Path: map[string]any{"id": "one"}}); err != nil {
		t.Fatalf("default policy should enable delete_widget: %v", err)
	}
	if _, _, err := manager.configure(ConfigureToolsInput{RemoveEndpoints: []string{"delete_widget"}}); err != nil {
		t.Fatal(err)
	}
	_, err = runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Disabled batch",
		Items: []ExtensionBatchItem{{
			ID:        "disabled",
			Operation: "delete_widget",
			Input:     ToolInput{Path: map[string]any{"id": "one"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled operation error = %v", err)
	}
	if len(confirmer.confirmations) != 0 {
		t.Fatalf("disabled batch requested confirmation: %+v", confirmer.confirmations)
	}
}

func TestExtensionBatchRechecksPolicyAfterApprovalBeforeCredentialRead(t *testing.T) {
	store := &fakeKeychainStore{values: map[string][]byte{}}
	prompter := &fakeCredentialPrompter{value: []byte("secret")}
	runtime := extensionRuntimeForTest(t, "https://api.example.test", store, prompter, &fakeActionConfirmer{})
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	operations := make([]OperationDescriptor, 0, len(runtime.operations))
	for _, operation := range runtime.operations {
		operations = append(operations, operation)
	}
	if err := writeJSONFileAtomically(catalogPath, ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{{
			ID:          "record-writes",
			Title:       "Record writes",
			Description: "Write test records.",
			Risk:        "write",
			Endpoints:   []string{"delete_widget"},
		}},
	}, true); err != nil {
		t.Fatal(err)
	}
	policy, err := newToolPolicyManager(ResolvedToolPolicyConfig{
		UserConfigPath:  filepath.Join(directory, "policy.json"),
		CatalogPath:     catalogPath,
		DefaultFeatures: []string{"record-writes"},
	}, operations)
	if err != nil {
		t.Fatal(err)
	}
	runtime.policy = policy
	runtime.filter = policy.filter()
	confirmer := &fakeActionConfirmer{onConfirm: func() {
		if _, _, err := policy.configure(ConfigureToolsInput{RemoveEndpoints: []string{"delete_widget"}}); err != nil {
			t.Fatalf("disable policy after approval: %v", err)
		}
	}}
	runtime.confirmer = confirmer
	auth := ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
		CredentialMissing: true,
	}
	runtime.config.Auth = auth
	runtime.credentials = newCredentialManager(runtime.config.Name, auth, store, prompter)

	_, err = runtime.ExecuteBatch(context.Background(), ExtensionBatchRequest{
		Action: "Delete a record",
		Items: []ExtensionBatchItem{{
			ID:        "one",
			Operation: "delete_widget",
			Input:     ToolInput{Path: map[string]any{"id": "one"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "disabled or invalidated after approval") {
		t.Fatalf("post-approval policy error = %v", err)
	}
	if len(confirmer.confirmations) != 1 || store.getCalls != 0 || prompter.calls != 0 {
		t.Fatalf("protected boundary crossed: confirmations=%d keychainGets=%d prompts=%d", len(confirmer.confirmations), store.getCalls, prompter.calls)
	}
}

func extensionRuntimeForTest(t *testing.T, serverURL string, store KeychainStore, prompter CredentialPrompter, confirmer ActionConfirmer) *ExtensionRuntime {
	t.Helper()
	cfg := Config{
		Name:      "extension-test",
		Version:   "0.1.0",
		Spec:      filepath.Join("..", "..", "testdata", "openapi.yaml"),
		ServerURL: serverURL,
		Auth:      ResolvedAuthConfig{Type: "none"},
		Tools: ToolFilterConfig{Include: []string{
			"get_current_account",
			"delete_widget",
		}},
		MaxResponseChars: 20_000,
	}
	operations, err := LoadOperations(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &ExtensionRuntime{
		config:      cfg,
		credentials: newCredentialManager(cfg.Name, cfg.Auth, store, prompter),
		filter:      cfg.Tools,
		operations:  operationMap(operations),
		confirmer:   confirmer,
	}
}
