package apimcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const toolPolicyVersion = 1

type ResolvedToolPolicyConfig struct {
	UserConfigPath  string
	CatalogPath     string
	DefaultFeatures []string
}

type ToolFeatureCatalog struct {
	Version  int           `json:"version"`
	Features []ToolFeature `json:"features"`
}

type ToolFeature struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Risk        string   `json:"risk"`
	Endpoints   []string `json:"endpoints"`
}

type UserToolPolicy struct {
	Version           int      `json:"version"`
	EnabledFeatures   []string `json:"enabledFeatures"`
	EnabledEndpoints  []string `json:"enabledEndpoints"`
	DisabledEndpoints []string `json:"disabledEndpoints"`
}

type ConfigureToolsInput struct {
	AddFeatures     []string `json:"addFeatures,omitempty"`
	RemoveFeatures  []string `json:"removeFeatures,omitempty"`
	AddEndpoints    []string `json:"addEndpoints,omitempty"`
	RemoveEndpoints []string `json:"removeEndpoints,omitempty"`
}

type ToolFeatureStatus struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	Risk          string   `json:"risk"`
	Enabled       bool     `json:"enabled"`
	EndpointCount int      `json:"endpointCount"`
	Endpoints     []string `json:"endpoints"`
}

type ToolEndpointStatus struct {
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Method      string   `json:"method"`
	Path        string   `json:"path"`
	Enabled     bool     `json:"enabled"`
	Features    []string `json:"features,omitempty"`
}

type toolPolicyManager struct {
	mu         sync.Mutex
	config     ResolvedToolPolicyConfig
	catalog    ToolFeatureCatalog
	policy     UserToolPolicy
	operations []OperationDescriptor
}

func resolveToolPolicyConfig(input *ToolPolicyConfig, baseDir string) (*ResolvedToolPolicyConfig, error) {
	if input == nil {
		return nil, nil
	}
	directory := strings.TrimSpace(input.UserConfigDirectory)
	if directory == "" || directory == "." || directory == ".." || filepath.Base(directory) != directory {
		return nil, errors.New("toolPolicy.userConfigDirectory must be a single directory name")
	}
	fileName := strings.TrimSpace(input.UserConfigFile)
	if fileName == "" {
		fileName = "config.json"
	}
	if fileName == "." || fileName == ".." || filepath.Base(fileName) != fileName {
		return nil, errors.New("toolPolicy.userConfigFile must be a file name without directories")
	}
	if strings.TrimSpace(input.Catalog) == "" {
		return nil, errors.New("toolPolicy.catalog is required")
	}
	if isHTTPURL(input.Catalog) {
		return nil, errors.New("toolPolicy.catalog must be a local file")
	}
	userConfigRoot, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return &ResolvedToolPolicyConfig{
		UserConfigPath:  filepath.Join(userConfigRoot, directory, fileName),
		CatalogPath:     filepath.Join(baseDir, input.Catalog),
		DefaultFeatures: normalizeStringList(input.DefaultFeatures),
	}, nil
}

func newToolPolicyManager(config ResolvedToolPolicyConfig, operations []OperationDescriptor) (*toolPolicyManager, error) {
	catalog, err := loadToolFeatureCatalog(config.CatalogPath, operations)
	if err != nil {
		return nil, err
	}
	manager := &toolPolicyManager{
		config:     config,
		catalog:    catalog,
		operations: append([]OperationDescriptor(nil), operations...),
	}
	policy, err := manager.loadOrCreatePolicy()
	if err != nil {
		return nil, err
	}
	manager.policy = policy
	return manager, nil
}

func loadToolFeatureCatalog(path string, operations []OperationDescriptor) (ToolFeatureCatalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolFeatureCatalog{}, fmt.Errorf("read tool feature catalog: %w", err)
	}
	var catalog ToolFeatureCatalog
	if err := decodeStrictJSON(data, &catalog); err != nil {
		return ToolFeatureCatalog{}, fmt.Errorf("parse tool feature catalog: %w", err)
	}
	if catalog.Version != toolPolicyVersion {
		return ToolFeatureCatalog{}, fmt.Errorf("unsupported tool feature catalog version %d", catalog.Version)
	}
	seen := map[string]bool{}
	for i := range catalog.Features {
		feature := &catalog.Features[i]
		feature.ID = strings.TrimSpace(feature.ID)
		feature.Title = strings.TrimSpace(feature.Title)
		feature.Description = strings.TrimSpace(feature.Description)
		feature.Risk = strings.ToLower(strings.TrimSpace(feature.Risk))
		feature.Endpoints = normalizeStringList(feature.Endpoints)
		if feature.ID == "" || feature.Title == "" || feature.Description == "" {
			return ToolFeatureCatalog{}, fmt.Errorf("feature %d must include id, title, and description", i+1)
		}
		if seen[feature.ID] {
			return ToolFeatureCatalog{}, fmt.Errorf("duplicate tool feature %q", feature.ID)
		}
		seen[feature.ID] = true
		if feature.Risk != "read" && feature.Risk != "write" {
			return ToolFeatureCatalog{}, fmt.Errorf("feature %q risk must be `read` or `write`", feature.ID)
		}
		if len(feature.Endpoints) == 0 {
			return ToolFeatureCatalog{}, fmt.Errorf("feature %q must include at least one endpoint selector", feature.ID)
		}
		for _, selector := range feature.Endpoints {
			if !selectorMatchesAnyOperation(selector, operations) {
				return ToolFeatureCatalog{}, fmt.Errorf("feature %q endpoint selector %q matches no OpenAPI operation", feature.ID, selector)
			}
		}
	}
	return catalog, nil
}

func (m *toolPolicyManager) loadOrCreatePolicy() (UserToolPolicy, error) {
	data, err := os.ReadFile(m.config.UserConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		policy := normalizeUserToolPolicy(UserToolPolicy{
			Version:         toolPolicyVersion,
			EnabledFeatures: append([]string(nil), m.config.DefaultFeatures...),
		})
		if err := m.validatePolicy(policy); err != nil {
			return UserToolPolicy{}, fmt.Errorf("invalid default tool policy: %w", err)
		}
		if err := writeJSONFileAtomically(m.config.UserConfigPath, policy, true); err != nil {
			if errors.Is(err, os.ErrExist) {
				return m.loadOrCreatePolicy()
			}
			return UserToolPolicy{}, fmt.Errorf("create default tool configuration: %w", err)
		}
		return policy, nil
	}
	if err != nil {
		return UserToolPolicy{}, fmt.Errorf("read user tool configuration: %w", err)
	}
	if err := os.Chmod(filepath.Dir(m.config.UserConfigPath), 0o700); err != nil {
		return UserToolPolicy{}, fmt.Errorf("protect user tool configuration directory: %w", err)
	}
	var policy UserToolPolicy
	if err := decodeStrictJSON(data, &policy); err != nil {
		return UserToolPolicy{}, fmt.Errorf("parse user tool configuration: %w", err)
	}
	policy = normalizeUserToolPolicy(policy)
	if err := m.validatePolicy(policy); err != nil {
		return UserToolPolicy{}, fmt.Errorf("invalid user tool configuration: %w", err)
	}
	if err := os.Chmod(m.config.UserConfigPath, 0o600); err != nil {
		return UserToolPolicy{}, fmt.Errorf("protect user tool configuration: %w", err)
	}
	return policy, nil
}

func (m *toolPolicyManager) validatePolicy(policy UserToolPolicy) error {
	if policy.Version != toolPolicyVersion {
		return fmt.Errorf("unsupported version %d", policy.Version)
	}
	features := m.featureMap()
	for _, id := range policy.EnabledFeatures {
		if _, ok := features[id]; !ok {
			return fmt.Errorf("unknown feature %q", id)
		}
	}
	for _, selector := range append(append([]string(nil), policy.EnabledEndpoints...), policy.DisabledEndpoints...) {
		if !selectorMatchesAnyOperation(selector, m.operations) {
			return fmt.Errorf("endpoint selector %q matches no OpenAPI operation", selector)
		}
	}
	return nil
}

func (m *toolPolicyManager) filter() ToolFilterConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.filterLocked()
}

func (m *toolPolicyManager) filterLocked() ToolFilterConfig {
	features := m.featureMap()
	include := append([]string(nil), m.policy.EnabledEndpoints...)
	for _, id := range m.policy.EnabledFeatures {
		include = append(include, features[id].Endpoints...)
	}
	return ToolFilterConfig{
		Include:       normalizeStringList(include),
		Exclude:       append([]string(nil), m.policy.DisabledEndpoints...),
		DefaultPolicy: "deny",
	}
}

func (m *toolPolicyManager) configure(input ConfigureToolsInput) (UserToolPolicy, ToolFilterConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	input.AddFeatures = normalizeStringList(input.AddFeatures)
	input.RemoveFeatures = normalizeStringList(input.RemoveFeatures)
	input.AddEndpoints = normalizeStringList(input.AddEndpoints)
	input.RemoveEndpoints = normalizeStringList(input.RemoveEndpoints)
	if len(input.AddFeatures)+len(input.RemoveFeatures)+len(input.AddEndpoints)+len(input.RemoveEndpoints) == 0 {
		return UserToolPolicy{}, ToolFilterConfig{}, errors.New("configure_tools requires at least one requested change")
	}
	if overlap := firstOverlap(input.AddFeatures, input.RemoveFeatures); overlap != "" {
		return UserToolPolicy{}, ToolFilterConfig{}, fmt.Errorf("feature %q cannot be added and removed in the same request", overlap)
	}
	if overlap := firstOverlap(input.AddEndpoints, input.RemoveEndpoints); overlap != "" {
		return UserToolPolicy{}, ToolFilterConfig{}, fmt.Errorf("endpoint %q cannot be added and removed in the same request", overlap)
	}
	features := m.featureMap()
	for _, id := range append(append([]string(nil), input.AddFeatures...), input.RemoveFeatures...) {
		if _, ok := features[id]; !ok {
			return UserToolPolicy{}, ToolFilterConfig{}, fmt.Errorf("unknown feature %q", id)
		}
	}

	next := m.policy
	next.EnabledFeatures = applySetChanges(next.EnabledFeatures, input.AddFeatures, input.RemoveFeatures)
	next.EnabledEndpoints = applySetChanges(next.EnabledEndpoints, input.AddEndpoints, input.RemoveEndpoints)
	next.DisabledEndpoints = applySetChanges(next.DisabledEndpoints, input.RemoveEndpoints, input.AddEndpoints)
	next = normalizeUserToolPolicy(next)
	if err := m.validatePolicy(next); err != nil {
		return UserToolPolicy{}, ToolFilterConfig{}, err
	}
	if err := writeJSONFileAtomically(m.config.UserConfigPath, next, false); err != nil {
		return UserToolPolicy{}, ToolFilterConfig{}, fmt.Errorf("save user tool configuration: %w", err)
	}
	m.policy = next
	return cloneUserToolPolicy(next), m.filterLocked(), nil
}

func (m *toolPolicyManager) snapshot() (UserToolPolicy, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneUserToolPolicy(m.policy), m.config.UserConfigPath
}

func (m *toolPolicyManager) features() []ToolFeatureStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	enabled := stringSet(m.policy.EnabledFeatures)
	result := make([]ToolFeatureStatus, 0, len(m.catalog.Features))
	for _, feature := range m.catalog.Features {
		count := 0
		for _, operation := range m.operations {
			if matchesAny(feature.Endpoints, FilterCandidates(operation)) {
				count++
			}
		}
		result = append(result, ToolFeatureStatus{
			ID:            feature.ID,
			Title:         feature.Title,
			Description:   feature.Description,
			Risk:          feature.Risk,
			Enabled:       enabled[feature.ID],
			EndpointCount: count,
			Endpoints:     append([]string(nil), feature.Endpoints...),
		})
	}
	return result
}

func (m *toolPolicyManager) endpoints(query string, enabledOnly *bool, limit int) []ToolEndpointStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query = strings.ToLower(strings.TrimSpace(query))
	filter := m.filterLocked()
	result := make([]ToolEndpointStatus, 0, min(limit, len(m.operations)))
	for _, operation := range m.operations {
		enabled := ShouldExposeOperation(operation, filter)
		if enabledOnly != nil && enabled != *enabledOnly {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{operation.ToolName, operation.Title, operation.Description, operation.Method, operation.Path, operation.OperationID}, " "))
		if query != "" && !strings.Contains(haystack, query) {
			continue
		}
		features := []string{}
		for _, feature := range m.catalog.Features {
			if matchesAny(feature.Endpoints, FilterCandidates(operation)) {
				features = append(features, feature.ID)
			}
		}
		result = append(result, ToolEndpointStatus{
			Name:        operation.ToolName,
			Title:       operation.Title,
			Description: operation.Description,
			Method:      strings.ToUpper(operation.Method),
			Path:        operation.Path,
			Enabled:     enabled,
			Features:    features,
		})
		if len(result) == limit {
			break
		}
	}
	return result
}

func (m *toolPolicyManager) enabledEndpointCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	filter := m.filterLocked()
	count := 0
	for _, operation := range m.operations {
		if ShouldExposeOperation(operation, filter) {
			count++
		}
	}
	return count
}

func (m *toolPolicyManager) featureMap() map[string]ToolFeature {
	result := make(map[string]ToolFeature, len(m.catalog.Features))
	for _, feature := range m.catalog.Features {
		result[feature.ID] = feature
	}
	return result
}

func normalizeUserToolPolicy(policy UserToolPolicy) UserToolPolicy {
	policy.EnabledFeatures = sortedUniqueStrings(policy.EnabledFeatures)
	policy.EnabledEndpoints = sortedUniqueStrings(policy.EnabledEndpoints)
	policy.DisabledEndpoints = sortedUniqueStrings(policy.DisabledEndpoints)
	return policy
}

func cloneUserToolPolicy(policy UserToolPolicy) UserToolPolicy {
	policy.EnabledFeatures = append([]string{}, policy.EnabledFeatures...)
	policy.EnabledEndpoints = append([]string{}, policy.EnabledEndpoints...)
	policy.DisabledEndpoints = append([]string{}, policy.DisabledEndpoints...)
	return policy
}

func sortedUniqueStrings(values []string) []string {
	set := stringSet(normalizeStringList(values))
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func applySetChanges(current, additions, removals []string) []string {
	set := stringSet(current)
	for _, value := range removals {
		delete(set, value)
	}
	for _, value := range additions {
		set[value] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func firstOverlap(left, right []string) string {
	rightSet := stringSet(right)
	for _, value := range left {
		if rightSet[value] {
			return value
		}
	}
	return ""
}

func selectorMatchesAnyOperation(selector string, operations []OperationDescriptor) bool {
	for _, operation := range operations {
		if matchesAny([]string{selector}, FilterCandidates(operation)) {
			return true
		}
	}
	return false
}

func writeJSONFileAtomically(path string, value any, exclusive bool) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	if exclusive {
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if err := encodeAndCloseJSON(file, value); err != nil {
			_ = os.Remove(path)
			return err
		}
		return nil
	}
	temporary, err := os.CreateTemp(directory, ".config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := encodeAndCloseJSON(temporary, value); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func encodeAndCloseJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func decodeStrictJSON(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
