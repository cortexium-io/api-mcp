package apimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCreateServerRegistersOpenAPIOperationsAsMCPTools(t *testing.T) {
	cfg := baseTestConfig
	cfg.Spec = filepath.Join("..", "..", "examples", "petstore-openapi.json")

	tools := listToolsForTest(t, cfg)
	tool := findTool(tools, "get_pet_by_id")
	if tool == nil {
		t.Fatal("missing get_pet_by_id tool")
	}
	schema := toolInputSchema(t, tool)
	properties := schemaProperties(t, schema)
	if _, ok := properties["path"]; !ok {
		t.Fatal("expected path input")
	}
	if required := stringSlice(schema["required"]); !reflect.DeepEqual(required, []string{"path"}) {
		t.Fatalf("required = %#v, want path", required)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Fatalf("GET tool annotations = %#v, want read-only", tool.Annotations)
	}
	if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
		t.Fatalf("GET tool annotations = %#v, want open-world", tool.Annotations)
	}
}

func TestDiagnosticsReportsRuntimeWithoutCredentialMaterial(t *testing.T) {
	cfg := baseTestConfig
	cfg.Spec = filepath.Join("..", "..", "examples", "petstore-openapi.json")
	cfg.Auth = ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
		CredentialMissing: true,
	}
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(*cfg.Auth.Keychain): []byte("top-secret-credential")}}
	result := callToolForTest(t, cfg, store, &fakeCredentialPrompter{}, diagnosticsToolName)
	if result.IsError {
		t.Fatalf("diagnostics failed: %+v", result.Content)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("top-secret-credential")) {
		t.Fatalf("diagnostics leaked credential: %s", data)
	}
	if !bytes.Contains(data, []byte(`"operationCount"`)) || !bytes.Contains(data, []byte(`"maxResponseBytes"`)) {
		t.Fatalf("diagnostics omitted runtime details: %s", data)
	}
	if !bytes.Contains(data, []byte(`"storedInKeychain":true`)) {
		t.Fatalf("diagnostics did not report stored credential status: %s", data)
	}
}

func TestDiagnosticsToolNameIsReserved(t *testing.T) {
	if !isReservedToolName(diagnosticsToolName) {
		t.Fatalf("%s must be reserved", diagnosticsToolName)
	}
}

func TestCreateServerMarksMutatingOpenAPIOperationsAsDestructive(t *testing.T) {
	cfg := Config{
		Name:             "example-api-mcp",
		Version:          "0.1.0",
		Spec:             filepath.Join("..", "..", "testdata", "openapi.yaml"),
		Auth:             ResolvedAuthConfig{Type: "none"},
		Tools:            ToolFilterConfig{Include: []string{"update_widget"}},
		MaxResponseChars: 20_000,
	}

	tool := findTool(listToolsForTest(t, cfg), "update_widget")
	if tool == nil {
		t.Fatal("missing update_widget tool")
	}
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
		t.Fatalf("PUT tool annotations = %#v, want mutating", tool.Annotations)
	}
	if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
		t.Fatalf("PUT tool annotations = %#v, want destructive", tool.Annotations)
	}
	if tool.Annotations.OpenWorldHint == nil || !*tool.Annotations.OpenWorldHint {
		t.Fatalf("PUT tool annotations = %#v, want open-world", tool.Annotations)
	}
}

func TestCreateServerRegistersFilteredToolsWithPluralHeadersInput(t *testing.T) {
	cfg := Config{
		Name:             "example-api-mcp",
		Version:          "0.1.0",
		Spec:             filepath.Join("..", "..", "testdata", "openapi.yaml"),
		Auth:             ResolvedAuthConfig{Type: "apiKey", In: "headers", Name: "X-API-Key", Value: "test-token"},
		Headers:          map[string]string{},
		Tools:            ToolFilterConfig{Include: []string{"get_widget"}},
		MaxResponseChars: 20_000,
	}

	tools := listToolsForTest(t, cfg)
	if findTool(tools, diagnosticsToolName) == nil {
		t.Fatalf("missing %s tool", diagnosticsToolName)
	}
	if findTool(tools, "update_widget") != nil {
		t.Fatal("filtered update_widget tool was registered")
	}
	tool := findTool(tools, "get_widget")
	if tool == nil {
		t.Fatal("missing get_widget tool")
	}
	if got, want := tool.Name, "get_widget"; got != want {
		t.Fatalf("tool = %q, want %q", got, want)
	}
	properties := schemaProperties(t, toolInputSchema(t, tool))
	for _, key := range []string{"path", "query", "headers"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("expected %s input", key)
		}
	}
	if _, ok := properties["header"]; ok {
		t.Fatal("did not expect singular header input")
	}
	if _, ok := properties["cookie"]; ok {
		t.Fatal("did not expect singular cookie input")
	}
}

func TestCreateServerRegistersCredentialToolsForPromptEnabledKeychain(t *testing.T) {
	cfg := baseTestConfig
	cfg.Spec = filepath.Join("..", "..", "examples", "petstore-openapi.json")
	cfg.Auth = ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
		CredentialMissing: true,
	}

	tools := listToolsForTestWithRuntime(t, cfg, &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{})
	if findTool(tools, credentialStatusToolName) == nil {
		t.Fatalf("missing %s tool", credentialStatusToolName)
	}
	setup := findTool(tools, credentialSetupToolName)
	if setup == nil {
		t.Fatalf("missing %s tool", credentialSetupToolName)
	}
	properties := schemaProperties(t, toolInputSchema(t, setup))
	if len(properties) != 0 {
		t.Fatalf("setup tool accepts unexpected arguments: %#v", properties)
	}
	if additional, ok := toolInputSchema(t, setup)["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("setup tool must reject all arguments: %#v", toolInputSchema(t, setup))
	}
}

func TestCredentialSetupToolStoresWithoutReturningCredential(t *testing.T) {
	cfg := baseTestConfig
	cfg.Spec = filepath.Join("..", "..", "examples", "petstore-openapi.json")
	cfg.Auth = ResolvedAuthConfig{
		Type:              "apiKey",
		In:                "headers",
		Name:              "X-API-Key",
		Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
		CredentialMissing: true,
	}
	store := &fakeKeychainStore{values: map[string][]byte{}}
	result := callToolForTest(t, cfg, store, &fakeCredentialPrompter{value: []byte("top-secret-credential")}, credentialSetupToolName)
	if result.IsError {
		t.Fatalf("setup tool failed: %+v", result.Content)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("top-secret-credential")) {
		t.Fatalf("setup result leaked credential: %s", data)
	}
	if got, want := store.setCalls, 1; got != want {
		t.Fatalf("Keychain writes = %d, want %d", got, want)
	}
}

func TestMutatingOperationRequiresLocalConfirmationBeforeCredentialsOrHTTP(t *testing.T) {
	var requestCount int
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer apiServer.Close()

	ref := KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true}
	store := &fakeKeychainStore{values: map[string][]byte{}}
	prompter := &fakeCredentialPrompter{value: []byte("secret")}
	confirmer := &fakeActionConfirmer{err: ErrActionConfirmationCanceled}
	cfg := Config{
		Name:      "confirmation-test",
		Version:   "0.1.0",
		Spec:      filepath.Join("..", "..", "testdata", "openapi.yaml"),
		ServerURL: apiServer.URL,
		Auth: ResolvedAuthConfig{
			Type:              "apiKey",
			In:                "headers",
			Name:              "X-API-Key",
			Keychain:          &ref,
			CredentialMissing: true,
		},
		Tools:            ToolFilterConfig{Include: []string{"create_widget"}},
		MaxResponseChars: 20_000,
	}

	result := callToolWithArgsForTest(t, cfg, store, prompter, confirmer, "create_widget", map[string]any{
		"body": map[string]any{"name": "Blocked"},
	})
	if !result.IsError {
		t.Fatal("canceled confirmation should reject the write")
	}
	if requestCount != 0 || store.getCalls != 0 || store.setCalls != 0 || prompter.calls != 0 {
		t.Fatalf("canceled write crossed a protected boundary: requests=%d keychainGets=%d keychainSets=%d prompts=%d", requestCount, store.getCalls, store.setCalls, prompter.calls)
	}
	if got, want := len(confirmer.confirmations), 1; got != want {
		t.Fatalf("confirmations = %d, want %d", got, want)
	}
	confirmation := confirmer.confirmations[0]
	if confirmation.Action != "POST create_widget" || confirmation.Destination != apiServer.URL+"/widgets" {
		t.Fatalf("unexpected confirmation: %+v", confirmation)
	}
}

func TestReadOperationDoesNotRequireLocalConfirmation(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer apiServer.Close()
	cfg := Config{
		Name:             "confirmation-test",
		Version:          "0.1.0",
		Spec:             filepath.Join("..", "..", "testdata", "openapi.yaml"),
		ServerURL:        apiServer.URL,
		Auth:             ResolvedAuthConfig{Type: "none"},
		Tools:            ToolFilterConfig{Include: []string{"get_current_account"}},
		MaxResponseChars: 20_000,
	}
	confirmer := &fakeActionConfirmer{err: ErrActionConfirmationCanceled}
	result := callToolWithArgsForTest(t, cfg, &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, confirmer, "get_current_account", nil)
	if result.IsError {
		t.Fatalf("read operation was blocked by the write confirmer: %+v", result.Content)
	}
	if len(confirmer.confirmations) != 0 {
		t.Fatalf("read operation requested confirmation: %+v", confirmer.confirmations)
	}
}

func TestGeneratedToolRejectsUndeclaredInputBeforeHTTP(t *testing.T) {
	var requestCount int
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer apiServer.Close()
	cfg := Config{
		Name:             "validation-test",
		Version:          "0.1.0",
		Spec:             filepath.Join("..", "..", "testdata", "openapi.yaml"),
		ServerURL:        apiServer.URL,
		Auth:             ResolvedAuthConfig{Type: "none"},
		Tools:            ToolFilterConfig{Include: []string{"get_current_account"}},
		MaxResponseChars: 20_000,
	}
	result := callToolWithArgsForTest(t, cfg, &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, &fakeActionConfirmer{}, "get_current_account", map[string]any{
		"query": map[string]any{"undeclared": "value"},
	})
	if !result.IsError || requestCount != 0 {
		t.Fatalf("undeclared input was forwarded: error=%v requests=%d", result.IsError, requestCount)
	}
}

func TestConfigureToolsRefreshesTheLiveMCPToolList(t *testing.T) {
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	catalog := ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{
			{ID: "read-only", Title: "Read-only", Description: "Read API data.", Risk: "read", Endpoints: []string{"GET *"}},
			{ID: "widget-writes", Title: "Widget writes", Description: "Create widgets.", Risk: "write", Endpoints: []string{"create_widget"}},
		},
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "user", "config.json")
	cfg := Config{
		Name:             "policy-test",
		Version:          "0.1.0",
		Spec:             filepath.Join("..", "..", "testdata", "openapi.yaml"),
		Auth:             ResolvedAuthConfig{Type: "none"},
		Headers:          map[string]string{},
		ToolPolicy:       &ResolvedToolPolicyConfig{UserConfigPath: configPath, CatalogPath: catalogPath, DefaultFeatures: []string{"read-only"}},
		MaxResponseChars: 20_000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, err := createServer(ctx, cfg, &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{}, &fakeActionConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	toolsBefore, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{toolConfigurationToolName, toolFeaturesToolName, toolSearchToolName, configureToolsToolName} {
		if findTool(toolsBefore.Tools, name) == nil {
			t.Fatalf("missing policy management tool %q", name)
		}
	}
	if findTool(toolsBefore.Tools, "create_widget") != nil {
		t.Fatal("write tool should not be exposed by the read-only default")
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      configureToolsToolName,
		Arguments: map[string]any{"addFeatures": []string{"widget-writes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("configure_tools failed: %+v", result.Content)
	}
	toolsAfter, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findTool(toolsAfter.Tools, "create_widget") == nil {
		t.Fatal("write tool was not added to the live MCP tool list")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      configureToolsToolName,
		Arguments: map[string]any{"removeFeatures": []string{"widget-writes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("configure_tools removal failed: %+v", result.Content)
	}
	toolsAfterRemoval, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findTool(toolsAfterRemoval.Tools, "create_widget") != nil {
		t.Fatal("write tool was not removed from the live MCP tool list")
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(`"widget-writes"`)) {
		t.Fatalf("removed feature remains in persisted policy: %s", persisted)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestProfileAwareServerIsolatesCredentialsAndPolicies(t *testing.T) {
	var requestsMu sync.Mutex
	var receivedTokens []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		token := req.Header.Get("X-API-Key")
		requestsMu.Lock()
		receivedTokens = append(receivedTokens, token)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":"` + token + `"}`))
	}))
	defer apiServer.Close()

	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	catalog := ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{
			{ID: "read-only", Title: "Read-only", Description: "Read API data.", Risk: "read", Endpoints: []string{"GET *"}},
			{ID: "widget-writes", Title: "Widget writes", Description: "Create widgets.", Risk: "write", Endpoints: []string{"create_widget"}},
		},
	}
	catalogData, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, catalogData, 0o600); err != nil {
		t.Fatal(err)
	}
	userDirectory := filepath.Join(directory, "user")
	defaultRef := KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true}
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(defaultRef): []byte("default-secret")}}
	prompter := &fakeCredentialPrompter{value: []byte("company-b-secret")}
	cfg := Config{
		Name:      "profile-server-test",
		Version:   "0.1.0",
		Spec:      filepath.Join("..", "..", "testdata", "openapi.yaml"),
		ServerURL: apiServer.URL,
		Auth: ResolvedAuthConfig{
			Type:              "apiKey",
			In:                "headers",
			Name:              "X-API-Key",
			Keychain:          &defaultRef,
			CredentialMissing: true,
		},
		Headers: map[string]string{},
		ToolPolicy: &ResolvedToolPolicyConfig{
			UserConfigPath:  filepath.Join(userDirectory, "config.json"),
			CatalogPath:     catalogPath,
			DefaultFeatures: []string{"read-only"},
		},
		Profiles: &ResolvedProfilesConfig{
			RegistryPath:       filepath.Join(userDirectory, "accounts.json"),
			ProfilesDirectory:  filepath.Join(userDirectory, "profiles"),
			DefaultProfileID:   "default",
			DefaultProfileName: "Default Company",
		},
		MaxResponseChars: 20_000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, err := createServer(ctx, cfg, store, prompter, &fakeActionConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() { errCh <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{listProfilesToolName, addProfileToolName, renameProfileToolName, removeProfileToolName, configureToolsToolName} {
		if findTool(tools.Tools, name) == nil {
			t.Fatalf("missing profile tool %q", name)
		}
	}
	readTool := findTool(tools.Tools, "get_current_account")
	if readTool == nil {
		t.Fatal("missing default read tool")
	}
	if _, ok := schemaProperties(t, toolInputSchema(t, readTool))["profile"]; !ok {
		t.Fatal("profile-aware API tool schema is missing profile")
	}
	if findTool(tools.Tools, "create_widget") != nil {
		t.Fatal("write tool should not be exposed while all profiles are read-only")
	}

	addResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      addProfileToolName,
		Arguments: map[string]any{"id": "company-b", "name": "Company B"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if addResult.IsError {
		t.Fatalf("add profile failed: %+v", addResult.Content)
	}
	addData, err := json.Marshal(addResult)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(addData, []byte("company-b-secret")) {
		t.Fatalf("add profile leaked credential: %s", addData)
	}
	if got, want := store.lastRef.Account, "profile:company-b"; got != want {
		t.Fatalf("new profile Keychain account = %q, want %q", got, want)
	}

	missingProfile, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_current_account", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !missingProfile.IsError {
		t.Fatal("API call without a profile should fail when multiple profiles exist")
	}
	requestsMu.Lock()
	requestCount := len(receivedTokens)
	requestsMu.Unlock()
	if requestCount != 0 {
		t.Fatalf("ambiguous profile call reached the API; requests = %d", requestCount)
	}

	readResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_current_account",
		Arguments: map[string]any{"profile": "company-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if readResult.IsError {
		t.Fatalf("profile read failed: %+v", readResult.Content)
	}
	readData, err := json.Marshal(readResult)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(readData, []byte("company-b-secret")) {
		t.Fatalf("profile API response leaked credential: %s", readData)
	}
	if !bytes.Contains(readData, []byte(`"id":"company-b"`)) {
		t.Fatalf("profile API response did not identify the account: %s", readData)
	}

	configureResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: configureToolsToolName,
		Arguments: map[string]any{
			"profile":     "company-b",
			"addFeatures": []string{"widget-writes"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if configureResult.IsError {
		t.Fatalf("profile tool configuration failed: %+v", configureResult.Content)
	}
	tools, err = session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findTool(tools.Tools, "create_widget") == nil {
		t.Fatal("write tool enabled for one profile was not added to the global MCP tool list")
	}

	defaultWrite, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_widget",
		Arguments: map[string]any{"profile": "default", "body": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !defaultWrite.IsError {
		t.Fatal("write enabled for company-b should be rejected for default")
	}
	requestsMu.Lock()
	requestCount = len(receivedTokens)
	requestsMu.Unlock()
	if requestCount != 1 {
		t.Fatalf("blocked cross-profile write reached the API; requests = %d", requestCount)
	}

	companyWrite, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "create_widget",
		Arguments: map[string]any{"profile": "company-b", "body": map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if companyWrite.IsError {
		t.Fatalf("enabled profile write failed: %+v", companyWrite.Content)
	}
	requestsMu.Lock()
	tokens := append([]string(nil), receivedTokens...)
	requestsMu.Unlock()
	if !reflect.DeepEqual(tokens, []string{"company-b-secret", "company-b-secret"}) {
		t.Fatalf("API tokens = %#v, want company-b credential only", tokens)
	}

	removeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: removeProfileToolName,
		Arguments: map[string]any{
			"profile":                 "company-b",
			"deleteCredential":        true,
			"deleteToolConfiguration": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if removeResult.IsError {
		t.Fatalf("remove profile failed: %+v", removeResult.Content)
	}
	tools, err = session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if findTool(tools.Tools, "create_widget") != nil {
		t.Fatal("write tool remained after removing its only enabled profile")
	}
	if store.deleteCalls != 1 {
		t.Fatalf("Keychain deletes = %d, want 1", store.deleteCalls)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func listToolsForTest(t *testing.T, cfg Config) []*mcp.Tool {
	return listToolsForTestWithRuntime(t, cfg, platformKeychainStore(), platformCredentialPrompter())
}

func listToolsForTestWithRuntime(t *testing.T, cfg Config, store KeychainStore, prompter CredentialPrompter) []*mcp.Tool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := createServer(ctx, cfg, store, prompter, &fakeActionConfirmer{})
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return result.Tools
}

func callToolForTest(t *testing.T, cfg Config, store KeychainStore, prompter CredentialPrompter, name string) *mcp.CallToolResult {
	return callToolWithArgsForTest(t, cfg, store, prompter, &fakeActionConfirmer{}, name, nil)
}

func callToolWithArgsForTest(t *testing.T, cfg Config, store KeychainStore, prompter CredentialPrompter, confirmer ActionConfirmer, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	if arguments == nil {
		arguments = map[string]any{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := createServer(ctx, cfg, store, prompter, confirmer)
	if err != nil {
		t.Fatal(err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return result
}

func findTool(tools []*mcp.Tool, name string) *mcp.Tool {
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

func toolInputSchema(t *testing.T, tool *mcp.Tool) map[string]any {
	t.Helper()
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		t.Fatalf("input schema has type %T", tool.InputSchema)
	}
	return schema
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T", schema["properties"])
	}
	return properties
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

type fakeActionConfirmer struct {
	err           error
	confirmations []ActionConfirmation
	onConfirm     func()
}

func (f *fakeActionConfirmer) Confirm(_ context.Context, confirmation ActionConfirmation) error {
	f.confirmations = append(f.confirmations, confirmation)
	if f.onConfirm != nil {
		f.onConfirm()
	}
	return f.err
}
