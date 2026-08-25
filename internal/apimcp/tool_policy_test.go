package apimcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestToolPolicyCreatesReadOnlyDefaultConfiguration(t *testing.T) {
	manager, configPath := newTestToolPolicyManager(t)

	current, gotPath := manager.snapshot()
	if gotPath != configPath {
		t.Fatalf("config path = %q, want %q", gotPath, configPath)
	}
	if !reflect.DeepEqual(current.EnabledFeatures, []string{"read-only"}) {
		t.Fatalf("enabled features = %#v, want read-only", current.EnabledFeatures)
	}
	if current.Version != toolPolicyVersion {
		t.Fatalf("version = %d, want %d", current.Version, toolPolicyVersion)
	}

	filter := manager.filter()
	if !ShouldExposeOperation(testToolPolicyOperations()[0], filter) {
		t.Fatal("read endpoint should be enabled by the default feature")
	}
	if ShouldExposeOperation(testToolPolicyOperations()[1], filter) {
		t.Fatal("write endpoint should be disabled by default")
	}
	if got, want := manager.enabledEndpointCount(), 1; got != want {
		t.Fatalf("enabled endpoint count = %d, want %d", got, want)
	}

	fileInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("config permissions = %o, want %o", got, want)
	}
	directoryInfo, err := os.Stat(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := directoryInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("config directory permissions = %o, want %o", got, want)
	}
}

func TestToolPolicyConfigurePersistsFeatureAndEndpointOverrides(t *testing.T) {
	manager, configPath := newTestToolPolicyManager(t)

	current, filter, err := manager.configure(ConfigureToolsInput{AddFeatures: []string{"record-writes"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(current.EnabledFeatures, []string{"read-only", "record-writes"}) {
		t.Fatalf("enabled features = %#v", current.EnabledFeatures)
	}
	if !ShouldExposeOperation(testToolPolicyOperations()[1], filter) {
		t.Fatal("write endpoint should be enabled after adding its feature")
	}

	_, filter, err = manager.configure(ConfigureToolsInput{RemoveEndpoints: []string{"GET /records/{recordId}"}})
	if err != nil {
		t.Fatal(err)
	}
	if ShouldExposeOperation(testToolPolicyOperations()[0], filter) {
		t.Fatal("explicit endpoint block should override an enabled feature")
	}

	current, filter, err = manager.configure(ConfigureToolsInput{AddEndpoints: []string{"GET /records/{recordId}"}})
	if err != nil {
		t.Fatal(err)
	}
	if !ShouldExposeOperation(testToolPolicyOperations()[0], filter) {
		t.Fatal("adding an endpoint should remove the same explicit block")
	}
	if len(current.DisabledEndpoints) != 0 {
		t.Fatalf("disabled endpoints = %#v, want none", current.DisabledEndpoints)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted UserToolPolicy
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, current) {
		t.Fatalf("persisted policy = %#v, want %#v", persisted, current)
	}
}

func TestToolPolicyRejectsUnknownChangesWithoutOverwritingConfiguration(t *testing.T) {
	manager, configPath := newTestToolPolicyManager(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := manager.configure(ConfigureToolsInput{AddFeatures: []string{"unknown"}}); err == nil {
		t.Fatal("expected unknown feature error")
	}
	if _, _, err := manager.configure(ConfigureToolsInput{AddEndpoints: []string{"DELETE /everything"}}); err == nil {
		t.Fatal("expected unknown endpoint error")
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("invalid changes modified the configuration\nbefore: %s\nafter: %s", before, after)
	}
}

func TestToolPolicyRejectsUnknownUserConfigurationFields(t *testing.T) {
	manager, configPath := newTestToolPolicyManager(t)
	if manager == nil {
		t.Fatal("missing manager")
	}
	if err := os.WriteFile(configPath, []byte(`{
  "version": 1,
  "enabledFeatures": ["read-only"],
  "enabledEndpoints": [],
  "disabledEndpoint": ["create_record"]
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := newToolPolicyManager(ResolvedToolPolicyConfig{
		UserConfigPath:  configPath,
		CatalogPath:     manager.config.CatalogPath,
		DefaultFeatures: []string{"read-only"},
	}, testToolPolicyOperations())
	if err == nil {
		t.Fatal("expected unknown user configuration field to be rejected")
	}
}

func TestToolPolicyExplainsFeaturesAndSearchesEndpoints(t *testing.T) {
	manager, _ := newTestToolPolicyManager(t)

	features := manager.features()
	if got, want := len(features), 2; got != want {
		t.Fatalf("feature count = %d, want %d", got, want)
	}
	if !features[0].Enabled || features[0].Risk != "read" || features[0].Description == "" {
		t.Fatalf("unexpected read-only feature: %+v", features[0])
	}

	endpoints := manager.endpoints("create record", nil, 50)
	if got, want := len(endpoints), 1; got != want {
		t.Fatalf("endpoint count = %d, want %d", got, want)
	}
	if endpoints[0].Name != "create_record" || endpoints[0].Enabled || !reflect.DeepEqual(endpoints[0].Features, []string{"record-writes"}) {
		t.Fatalf("unexpected endpoint explanation: %+v", endpoints[0])
	}
}

func newTestToolPolicyManager(t *testing.T) (*toolPolicyManager, string) {
	t.Helper()
	directory := t.TempDir()
	catalogPath := filepath.Join(directory, "features.json")
	catalog := ToolFeatureCatalog{
		Version: toolPolicyVersion,
		Features: []ToolFeature{
			{
				ID:          "read-only",
				Title:       "Read-only access",
				Description: "Inspect records without changing the API.",
				Risk:        "read",
				Endpoints:   []string{"GET *"},
			},
			{
				ID:          "record-writes",
				Title:       "Create records",
				Description: "Create records in the API.",
				Risk:        "write",
				Endpoints:   []string{"create_record"},
			},
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
	manager, err := newToolPolicyManager(ResolvedToolPolicyConfig{
		UserConfigPath:  configPath,
		CatalogPath:     catalogPath,
		DefaultFeatures: []string{"read-only"},
	}, testToolPolicyOperations())
	if err != nil {
		t.Fatal(err)
	}
	return manager, configPath
}

func testToolPolicyOperations() []OperationDescriptor {
	return []OperationDescriptor{
		{
			ToolName:    "get_record",
			OperationID: "getRecord",
			Method:      "get",
			Path:        "/records/{recordId}",
			Title:       "Get record",
			Description: "Get one record.",
			Tags:        []string{"records"},
		},
		{
			ToolName:    "create_record",
			OperationID: "createRecord",
			Method:      "post",
			Path:        "/records",
			Title:       "Create record",
			Description: "Create a record.",
			Tags:        []string{"records"},
		},
	}
}
