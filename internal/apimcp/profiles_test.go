package apimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProfileManagerMigratesExistingDefaultAndIsolatesNewProfile(t *testing.T) {
	cfg, registryPath := newTestProfileConfig(t)
	existingPolicy := UserToolPolicy{
		Version:           toolPolicyVersion,
		EnabledFeatures:   []string{"read-only", "record-writes"},
		EnabledEndpoints:  []string{},
		DisabledEndpoints: []string{},
	}
	data, err := json.Marshal(existingPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.ToolPolicy.UserConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ToolPolicy.UserConfigPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	defaultRef := *cfg.Auth.Keychain
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(defaultRef): []byte("default-secret")}}
	prompter := &fakeCredentialPrompter{value: []byte("company-b-secret")}

	manager, err := newProfileManager(cfg, testToolPolicyOperations(), store, prompter)
	if err != nil {
		t.Fatal(err)
	}
	defaultRuntime, err := manager.resolve("default")
	if err != nil {
		t.Fatal(err)
	}
	if !ShouldExposeOperation(testToolPolicyOperations()[1], defaultRuntime.policy.filter()) {
		t.Fatal("existing default profile policy was not preserved")
	}
	_, defaultPath := defaultRuntime.policy.snapshot()
	if defaultPath != cfg.ToolPolicy.UserConfigPath {
		t.Fatalf("default policy path = %q, want %q", defaultPath, cfg.ToolPolicy.UserConfigPath)
	}

	status, err := manager.add(context.Background(), APIProfile{ID: "company-b", Name: "Company B"})
	if err != nil {
		t.Fatal(err)
	}
	if !status.CredentialStoredInKeychain || status.Name != "Company B" {
		t.Fatalf("unexpected new profile status: %+v", status)
	}
	if got, want := store.lastRef.Account, "profile:company-b"; got != want {
		t.Fatalf("new Keychain account = %q, want %q", got, want)
	}
	if got := string(store.values[keychainTestKey(store.lastRef)]); got != "company-b-secret" {
		t.Fatalf("new Keychain value = %q", got)
	}
	companyRuntime, err := manager.resolve("company-b")
	if err != nil {
		t.Fatal(err)
	}
	if ShouldExposeOperation(testToolPolicyOperations()[1], companyRuntime.policy.filter()) {
		t.Fatal("new profile should start read-only even when the migrated default permits writes")
	}
	_, companyPolicyPath := companyRuntime.policy.snapshot()
	if companyPolicyPath == defaultPath || companyPolicyPath != filepath.Join(cfg.Profiles.ProfilesDirectory, "company-b", "tools.json") {
		t.Fatalf("new profile policy path = %q", companyPolicyPath)
	}
	if _, err := manager.resolve(""); err == nil {
		t.Fatal("profile selection should be required after a second account is added")
	}

	renamed, err := manager.rename("company-b", "Renamed Company")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Renamed Company" {
		t.Fatalf("renamed profile = %+v", renamed)
	}
	removal, err := manager.remove("company-b", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if !removal.CredentialDeleted || !removal.ToolConfigurationDeleted || len(removal.Warnings) != 0 {
		t.Fatalf("unexpected removal: %+v", removal)
	}
	if store.deleteCalls != 1 {
		t.Fatalf("Keychain deletes = %d, want 1", store.deleteCalls)
	}
	if _, err := os.Stat(companyPolicyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("profile policy still exists: %v", err)
	}

	registryData, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registry ProfileRegistry
	if err := json.Unmarshal(registryData, &registry); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(registry.Profiles, []APIProfile{{ID: "default", Name: "Existing Company"}}) {
		t.Fatalf("persisted profiles = %#v", registry.Profiles)
	}
}

func TestProfileManagerRollsBackCanceledProfileSetup(t *testing.T) {
	cfg, registryPath := newTestProfileConfig(t)
	store := &fakeKeychainStore{values: map[string][]byte{}}
	manager, err := newProfileManager(cfg, testToolPolicyOperations(), store, &fakeCredentialPrompter{err: ErrCredentialSetupCanceled})
	if err != nil {
		t.Fatal(err)
	}

	_, err = manager.add(context.Background(), APIProfile{ID: "canceled", Name: "Canceled Company"})
	if err == nil || err.Error() != "credential setup was canceled; no credential was stored" {
		t.Fatalf("unexpected add error: %v", err)
	}
	if store.setCalls != 0 {
		t.Fatalf("Keychain writes = %d, want 0", store.setCalls)
	}
	profiles, err := manager.list()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(profiles), 1; got != want {
		t.Fatalf("profile count = %d, want %d", got, want)
	}
	if _, err := os.Stat(filepath.Join(cfg.Profiles.ProfilesDirectory, "canceled", "tools.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled profile policy still exists: %v", err)
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || bytes.Contains(data, []byte(`"canceled"`)) {
		t.Fatalf("canceled profile remains in registry: %s", data)
	}
}

func TestProfileManagerRefusesToRemoveLastProfile(t *testing.T) {
	cfg, _ := newTestProfileConfig(t)
	manager, err := newProfileManager(cfg, testToolPolicyOperations(), &fakeKeychainStore{values: map[string][]byte{}}, &fakeCredentialPrompter{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.remove("default", false, false); err == nil {
		t.Fatal("expected last-profile removal to fail")
	}
}

func newTestProfileConfig(t *testing.T) (Config, string) {
	t.Helper()
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	catalog := ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{
			{ID: "read-only", Title: "Read-only", Description: "Read records.", Risk: "read", Endpoints: []string{"GET *"}},
			{ID: "record-writes", Title: "Record writes", Description: "Create records.", Risk: "write", Endpoints: []string{"create_record"}},
		},
	}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	userDirectory := filepath.Join(directory, "user")
	registryPath := filepath.Join(userDirectory, "accounts.json")
	return Config{
		Name:    "profile-test",
		Version: "0.1.0",
		Auth: ResolvedAuthConfig{
			Type:              "apiKey",
			In:                "headers",
			Name:              "X-API-Key",
			Keychain:          &KeychainConfig{Service: "api-mcp.test", Account: "default", PromptOnMissing: true},
			CredentialMissing: true,
		},
		ToolPolicy: &ResolvedToolPolicyConfig{
			UserConfigPath:  filepath.Join(userDirectory, "config.json"),
			CatalogPath:     catalogPath,
			DefaultFeatures: []string{"read-only"},
		},
		Profiles: &ResolvedProfilesConfig{
			RegistryPath:       registryPath,
			ProfilesDirectory:  filepath.Join(userDirectory, "profiles"),
			DefaultProfileID:   "default",
			DefaultProfileName: "Existing Company",
		},
	}, registryPath
}
