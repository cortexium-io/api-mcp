package apimcp

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeConfigResolvesRelativeSpecAndEnvBackedCredentials(t *testing.T) {
	cfg, err := NormalizeConfig(ConfigInput{
		Name: "example",
		Spec: "./openapi.yaml",
		Auth: &AuthConfig{
			Type:     "apiKey",
			In:       "headers",
			Name:     "X-API-Key",
			ValueEnv: "API_KEY",
		},
		Headers: map[string]string{
			"X-Trace": "${TRACE_ID}",
		},
	}, "/tmp/config", mapLookup(map[string]string{
		"API_KEY":  "secret",
		"TRACE_ID": "trace-1",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := cfg.Spec, filepath.Join("/tmp/config", "openapi.yaml"); got != want {
		t.Fatalf("spec = %q, want %q", got, want)
	}
	if cfg.Auth.Type != "apiKey" || cfg.Auth.In != "headers" || cfg.Auth.Name != "X-API-Key" || cfg.Auth.Value != "secret" {
		t.Fatalf("unexpected auth config: %+v", cfg.Auth)
	}
	if got, want := cfg.Headers["X-Trace"], "trace-1"; got != want {
		t.Fatalf("header = %q, want %q", got, want)
	}
}

func TestNormalizeConfigUsesBuildVersionByDefault(t *testing.T) {
	previous := DefaultVersion
	DefaultVersion = "v9.8.7"
	t.Cleanup(func() { DefaultVersion = previous })

	cfg, err := NormalizeConfig(ConfigInput{Spec: "openapi.json"}, "/tmp/config", mapLookup(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Version, "v9.8.7"; got != want {
		t.Fatalf("version = %q, want %q", got, want)
	}
}

func TestNormalizeConfigRequiresExplicitRemoteSpecTrustAndPinnedAuthenticatedDestination(t *testing.T) {
	base := ConfigInput{Spec: "https://docs.example.com/openapi.json"}
	if _, err := NormalizeConfig(base, "/tmp/config", mapLookup(map[string]string{})); err == nil || !strings.Contains(err.Error(), "allowRemoteSpec") {
		t.Fatalf("remote spec without opt-in error = %v", err)
	}
	base.AllowRemoteSpec = true
	base.Auth = &AuthConfig{Type: "bearer", Token: "secret"}
	if _, err := NormalizeConfig(base, "/tmp/config", mapLookup(map[string]string{})); err == nil || !strings.Contains(err.Error(), "serverUrl") {
		t.Fatalf("authenticated remote spec without pinned server error = %v", err)
	}
	base.ServerURL = "https://api.example.com"
	cfg, err := NormalizeConfig(base, "/tmp/config", mapLookup(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowRemoteSpec || cfg.ServerURL != base.ServerURL {
		t.Fatalf("unexpected remote config: %+v", cfg)
	}
}

func TestNormalizeConfigRejectsInsecureRemoteSpec(t *testing.T) {
	_, err := NormalizeConfig(ConfigInput{Spec: "http://docs.example.com/openapi.json", AllowRemoteSpec: true}, "/tmp/config", mapLookup(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure remote spec error = %v", err)
	}
}

func TestResolveCompositeAuthHeadersTracksPlaceholderSecretsForRedaction(t *testing.T) {
	auth, err := resolveAuth(&AuthConfig{
		Type:    "headers",
		Headers: map[string]string{"X-Custom-Auth": "Token ${API_TOKEN}; tenant=public"},
	}, mapLookup(map[string]string{"API_TOKEN": "top-secret-token"}), &fakeKeychainStore{values: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := auth.Headers["X-Custom-Auth"], "Token top-secret-token; tenant=public"; got != want {
		t.Fatalf("resolved header = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(auth.RedactionValues, []string{"top-secret-token"}) {
		t.Fatalf("redaction values = %#v", auth.RedactionValues)
	}
}

func TestNormalizeConfigResolvesPersistentToolPolicy(t *testing.T) {
	cfg, err := NormalizeConfig(ConfigInput{
		Spec: "./openapi.yaml",
		ToolPolicy: &ToolPolicyConfig{
			UserConfigDirectory: "Example Plugin",
			UserConfigFile:      "tools.json",
			Catalog:             "./features.json",
			DefaultFeatures:     []string{" read-only ", ""},
		},
	}, "/tmp/config", mapLookup(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolPolicy == nil {
		t.Fatal("missing resolved tool policy")
	}
	if got, want := cfg.ToolPolicy.CatalogPath, filepath.Join("/tmp/config", "features.json"); got != want {
		t.Fatalf("catalog path = %q, want %q", got, want)
	}
	if got, want := filepath.Base(cfg.ToolPolicy.UserConfigPath), "tools.json"; got != want {
		t.Fatalf("user config file = %q, want %q", got, want)
	}
	if got, want := filepath.Base(filepath.Dir(cfg.ToolPolicy.UserConfigPath)), "Example Plugin"; got != want {
		t.Fatalf("user config directory = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(cfg.ToolPolicy.DefaultFeatures, []string{"read-only"}) {
		t.Fatalf("default features = %#v, want read-only", cfg.ToolPolicy.DefaultFeatures)
	}
}

func TestNormalizeConfigResolvesProfileRegistryBesideToolPolicy(t *testing.T) {
	cfg, err := normalizeConfig(ConfigInput{
		Spec: "./openapi.yaml",
		Auth: &AuthConfig{
			Type: "apiKey",
			In:   "headers",
			Name: "X-API-Key",
			Keychain: &KeychainConfig{
				Service:         "api-mcp.test",
				Account:         "default",
				PromptOnMissing: true,
			},
		},
		ToolPolicy: &ToolPolicyConfig{
			UserConfigDirectory: "Test API",
			UserConfigFile:      "tools.json",
			Catalog:             "features.json",
			DefaultFeatures:     []string{"read-only"},
		},
		Profiles: &ProfilesConfig{
			RegistryFile:       "accounts.json",
			DefaultProfileID:   "primary",
			DefaultProfileName: "Primary company",
		},
	}, "/tmp/config", mapLookup(map[string]string{}), &fakeKeychainStore{values: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles == nil {
		t.Fatal("missing resolved profiles config")
	}
	if got, want := filepath.Dir(cfg.Profiles.RegistryPath), filepath.Dir(cfg.ToolPolicy.UserConfigPath); got != want {
		t.Fatalf("registry directory = %q, want %q", got, want)
	}
	if got, want := filepath.Base(cfg.Profiles.RegistryPath), "accounts.json"; got != want {
		t.Fatalf("registry file = %q, want %q", got, want)
	}
	if got, want := cfg.Profiles.DefaultProfileID, "primary"; got != want {
		t.Fatalf("default profile ID = %q, want %q", got, want)
	}
}

func TestNormalizeConfigRejectsProfilesWithoutManagedPolicyAndPromptedKeychain(t *testing.T) {
	base := ConfigInput{
		Spec:     "openapi.json",
		Profiles: &ProfilesConfig{},
	}
	if _, err := NormalizeConfig(base, "/tmp/config", mapLookup(map[string]string{})); err == nil {
		t.Fatal("expected profiles without toolPolicy to fail")
	}

	base.ToolPolicy = &ToolPolicyConfig{
		UserConfigDirectory: "Test API",
		Catalog:             "features.json",
		DefaultFeatures:     []string{"read-only"},
	}
	base.Auth = &AuthConfig{
		Type: "apiKey",
		In:   "headers",
		Name: "X-API-Key",
		Keychain: &KeychainConfig{
			Service: "api-mcp.test",
			Account: "default",
		},
	}
	store := &fakeKeychainStore{values: map[string][]byte{keychainTestKey(*base.Auth.Keychain): []byte("secret")}}
	if _, err := normalizeConfig(base, "/tmp/config", mapLookup(map[string]string{}), store); err == nil {
		t.Fatal("expected profiles without promptOnMissing to fail")
	}
}

func TestNormalizeConfigRejectsInvalidToolPolicyPathsAndDefaultPolicy(t *testing.T) {
	for name, input := range map[string]ConfigInput{
		"invalid default policy": {
			Spec:  "openapi.json",
			Tools: ToolFilterConfig{DefaultPolicy: "maybe"},
		},
		"nested user directory": {
			Spec: "openapi.json",
			ToolPolicy: &ToolPolicyConfig{
				UserConfigDirectory: "Example/Plugin",
				Catalog:             "features.json",
			},
		},
		"remote catalog": {
			Spec: "openapi.json",
			ToolPolicy: &ToolPolicyConfig{
				UserConfigDirectory: "Example Plugin",
				Catalog:             "https://example.com/features.json",
			},
		},
		"fixed and managed tool filters": {
			Spec:  "openapi.json",
			Tools: ToolFilterConfig{Include: []string{"GET *"}},
			ToolPolicy: &ToolPolicyConfig{
				UserConfigDirectory: "Example Plugin",
				Catalog:             "features.json",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeConfig(input, "/tmp/config", mapLookup(map[string]string{})); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestNormalizeConfigRejectsMissingEnvBackedCredentials(t *testing.T) {
	_, err := NormalizeConfig(ConfigInput{
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type:     "bearer",
			TokenEnv: "API_TOKEN",
		},
	}, "/tmp/config", mapLookup(map[string]string{}))
	if err == nil {
		t.Fatal("expected missing env error")
	}
}

func TestNormalizeConfigFallsBackToKeychainWhenEnvironmentIsUnset(t *testing.T) {
	store := &fakeKeychainStore{values: map[string][]byte{
		keychainTestKey(KeychainConfig{Service: "api-mcp.example", Account: "default"}): []byte("keychain-secret"),
	}}
	cfg, err := normalizeConfig(ConfigInput{
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type:     "apiKey",
			In:       "headers",
			Name:     "X-API-Key",
			ValueEnv: "EXAMPLE_API_TOKEN",
			Keychain: &KeychainConfig{Service: "api-mcp.example", Account: "default"},
		},
	}, "/tmp/config", mapLookup(map[string]string{}), store)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Auth.Value, "keychain-secret"; got != want {
		t.Fatalf("auth value = %q, want %q", got, want)
	}
	if got, want := store.getCalls, 1; got != want {
		t.Fatalf("keychain get calls = %d, want %d", got, want)
	}
}

func TestNormalizeConfigPrefersEnvironmentOverKeychain(t *testing.T) {
	store := &fakeKeychainStore{values: map[string][]byte{
		keychainTestKey(KeychainConfig{Service: "api-mcp.example", Account: "default"}): []byte("keychain-secret"),
	}}
	cfg, err := normalizeConfig(ConfigInput{
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type:     "apiKey",
			In:       "headers",
			Name:     "X-API-Key",
			ValueEnv: "EXAMPLE_API_TOKEN",
			Keychain: &KeychainConfig{Service: "api-mcp.example", Account: "default"},
		},
	}, "/tmp/config", mapLookup(map[string]string{"EXAMPLE_API_TOKEN": "environment-secret"}), store)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Auth.Value, "environment-secret"; got != want {
		t.Fatalf("auth value = %q, want %q", got, want)
	}
	if store.getCalls != 0 {
		t.Fatalf("keychain should not be read when environment credential is present; calls = %d", store.getCalls)
	}
}

func TestNormalizeConfigReportsMissingKeychainItemWithoutSecretMaterial(t *testing.T) {
	store := &fakeKeychainStore{values: map[string][]byte{}}
	_, err := normalizeConfig(ConfigInput{
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type:     "bearer",
			Keychain: &KeychainConfig{Service: "api-mcp.test", Account: "missing"},
		},
	}, "/tmp/config", mapLookup(map[string]string{}), store)
	if err == nil {
		t.Fatal("expected missing keychain item error")
	}
	if !strings.Contains(err.Error(), "api-mcp keychain store") {
		t.Fatalf("error should explain enrollment: %v", err)
	}
}

func TestNormalizeConfigDefersMissingKeychainCredentialWhenPromptIsEnabled(t *testing.T) {
	store := &fakeKeychainStore{values: map[string][]byte{}}
	cfg, err := normalizeConfig(ConfigInput{
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type: "apiKey",
			In:   "headers",
			Name: "X-API-Key",
			Keychain: &KeychainConfig{
				Service:         "api-mcp.test",
				Account:         "missing",
				PromptOnMissing: true,
			},
		},
	}, "/tmp/config", mapLookup(map[string]string{}), store)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.CredentialMissing {
		t.Fatal("expected credential to remain unresolved until first use")
	}
	if cfg.Auth.Keychain == nil || !cfg.Auth.Keychain.PromptOnMissing {
		t.Fatalf("missing runtime Keychain reference: %+v", cfg.Auth.Keychain)
	}
	if cfg.Auth.Value != "" {
		t.Fatalf("unexpected resolved credential: %q", cfg.Auth.Value)
	}
	if store.getCalls != 0 {
		t.Fatalf("Keychain should not be read before first use; calls = %d", store.getCalls)
	}
}

func TestFindConfigPathRejectsUnexpectedArgument(t *testing.T) {
	_, _, err := findConfigPath([]string{"build"}, mapLookup(map[string]string{}))
	if err == nil {
		t.Fatal("expected unexpected argument error")
	}
}

func TestEmbeddedConfigForFileAbsolutizesSpecWithoutResolvingSecrets(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"spec": "./openapi.yaml",
		"auth": {
			"type": "apiKey",
			"in": "headers",
			"name": "X-API-Key",
			"valueEnv": "API_KEY",
			"keychain": {
				"service": "api-mcp.test",
				"account": "default"
			}
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	encoded, err := EmbeddedConfigForFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	var input ConfigInput
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if got, want := input.Spec, filepath.Join(dir, "openapi.yaml"); got != want {
		t.Fatalf("embedded spec = %q, want %q", got, want)
	}
	if input.Auth == nil || input.Auth.ValueEnv != "API_KEY" || input.Auth.Value != "" {
		t.Fatalf("embedded auth should keep valueEnv without resolving secret: %+v", input.Auth)
	}
	if input.Auth.Keychain == nil || input.Auth.Keychain.Service != "api-mcp.test" || input.Auth.Keychain.Account != "default" {
		t.Fatalf("embedded auth should preserve keychain reference: %+v", input.Auth.Keychain)
	}
}

func TestLoadConfigFromArgsUsesEmbeddedConfigWhenNoConfigIsProvided(t *testing.T) {
	previous := EmbeddedConfigBase64
	t.Cleanup(func() { EmbeddedConfigBase64 = previous })

	input := ConfigInput{
		Name: "embedded",
		Spec: "openapi.json",
		Auth: &AuthConfig{
			Type:     "apiKey",
			In:       "headers",
			Name:     "X-API-Key",
			ValueEnv: "API_KEY",
		},
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	EmbeddedConfigBase64 = base64.StdEncoding.EncodeToString(data)
	t.Setenv("API_KEY", "secret")
	t.Setenv("API_MCP_CONFIG", "")

	cfg, err := LoadConfigFromArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Name, "embedded"; got != want {
		t.Fatalf("name = %q, want %q", got, want)
	}
	if cfg.Auth.Value != "secret" {
		t.Fatalf("auth value = %q, want secret", cfg.Auth.Value)
	}
}

func TestEmbeddedConfigCanIncludeInlineCredentials(t *testing.T) {
	previous := EmbeddedConfigBase64
	t.Cleanup(func() { EmbeddedConfigBase64 = previous })

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{
		"spec": "openapi.json",
		"auth": {
			"type": "apiKey",
			"in": "headers",
			"name": "X-API-Key",
			"value": "embedded-secret"
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	encoded, err := EmbeddedConfigForFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	EmbeddedConfigBase64 = encoded
	t.Setenv("API_MCP_CONFIG", "")

	cfg, err := LoadConfigFromArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Type != "apiKey" || cfg.Auth.In != "headers" || cfg.Auth.Name != "X-API-Key" {
		t.Fatalf("unexpected auth config: %+v", cfg.Auth)
	}
	if got, want := cfg.Auth.Value, "embedded-secret"; got != want {
		t.Fatalf("auth value = %q, want %q", got, want)
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

type fakeKeychainStore struct {
	values      map[string][]byte
	getCalls    int
	setCalls    int
	deleteCalls int
	lastRef     KeychainConfig
	lastSet     []byte
}

func (s *fakeKeychainStore) Get(ref KeychainConfig) ([]byte, error) {
	s.getCalls++
	value, ok := s.values[keychainTestKey(ref)]
	if !ok {
		return nil, ErrKeychainItemNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *fakeKeychainStore) Set(ref KeychainConfig, value []byte) error {
	s.setCalls++
	s.lastRef = ref
	s.lastSet = append([]byte(nil), value...)
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[keychainTestKey(ref)] = append([]byte(nil), value...)
	return nil
}

func (s *fakeKeychainStore) Delete(ref KeychainConfig) error {
	s.deleteCalls++
	s.lastRef = ref
	delete(s.values, keychainTestKey(ref))
	return nil
}

func keychainTestKey(ref KeychainConfig) string {
	return ref.Service + "\x00" + ref.Account
}

var _ KeychainStore = (*fakeKeychainStore)(nil)
