package apimcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const profileRegistryVersion = 1

var profileIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type APIProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ProfileRegistry struct {
	Version  int          `json:"version"`
	Profiles []APIProfile `json:"profiles"`
}

type APIProfileStatus struct {
	ID                         string `json:"id"`
	Name                       string `json:"name"`
	CredentialAvailable        bool   `json:"credentialAvailable"`
	CredentialStoredInKeychain bool   `json:"credentialStoredInKeychain"`
	ToolConfigPath             string `json:"toolConfigPath"`
	EnabledEndpointCount       int    `json:"enabledEndpointCount"`
}

type APIProfileRemoval struct {
	Profile                  APIProfile `json:"profile"`
	CredentialDeleted        bool       `json:"credentialDeleted"`
	ToolConfigurationDeleted bool       `json:"toolConfigurationDeleted"`
	Warnings                 []string   `json:"warnings"`
}

type apiProfileRuntime struct {
	profile     APIProfile
	policy      *toolPolicyManager
	credentials *credentialManager
	keychain    KeychainConfig
}

type profileManager struct {
	mu         sync.Mutex
	config     ResolvedProfilesConfig
	basePolicy ResolvedToolPolicyConfig
	baseAuth   ResolvedAuthConfig
	appName    string
	store      KeychainStore
	prompter   CredentialPrompter
	operations []OperationDescriptor
	registry   ProfileRegistry
	runtimes   map[string]*apiProfileRuntime
}

func resolveProfilesConfig(input *ProfilesConfig, toolPolicy *ResolvedToolPolicyConfig, auth ResolvedAuthConfig) (*ResolvedProfilesConfig, error) {
	if input == nil {
		return nil, nil
	}
	if toolPolicy == nil {
		return nil, errors.New("profiles require `toolPolicy`")
	}
	if auth.Keychain == nil || !auth.Keychain.PromptOnMissing {
		return nil, errors.New("profiles require auth.keychain with promptOnMissing enabled")
	}
	switch auth.Type {
	case "apiKey", "bearer", "basic":
	default:
		return nil, fmt.Errorf("profiles do not support auth type %q", auth.Type)
	}

	registryFile := strings.TrimSpace(input.RegistryFile)
	if registryFile == "" {
		registryFile = "accounts.json"
	}
	if registryFile == "." || registryFile == ".." || filepath.Base(registryFile) != registryFile {
		return nil, errors.New("profiles.registryFile must be a file name without directories")
	}
	if registryFile == filepath.Base(toolPolicy.UserConfigPath) {
		return nil, errors.New("profiles.registryFile must differ from toolPolicy.userConfigFile")
	}
	defaultID := strings.TrimSpace(input.DefaultProfileID)
	if defaultID == "" {
		defaultID = "default"
	}
	if err := validateProfileID(defaultID); err != nil {
		return nil, fmt.Errorf("invalid profiles.defaultProfileId: %w", err)
	}
	defaultName := strings.TrimSpace(input.DefaultProfileName)
	if defaultName == "" {
		defaultName = "Default API account"
	}
	if err := validateProfileName(defaultName); err != nil {
		return nil, fmt.Errorf("invalid profiles.defaultProfileName: %w", err)
	}

	directory := filepath.Dir(toolPolicy.UserConfigPath)
	return &ResolvedProfilesConfig{
		RegistryPath:       filepath.Join(directory, registryFile),
		ProfilesDirectory:  filepath.Join(directory, "profiles"),
		DefaultProfileID:   defaultID,
		DefaultProfileName: defaultName,
	}, nil
}

func newProfileManager(cfg Config, operations []OperationDescriptor, store KeychainStore, prompter CredentialPrompter) (*profileManager, error) {
	if cfg.Profiles == nil || cfg.ToolPolicy == nil || cfg.Auth.Keychain == nil {
		return nil, errors.New("profile manager requires profiles, toolPolicy, and Keychain configuration")
	}
	manager := &profileManager{
		config:     *cfg.Profiles,
		basePolicy: *cfg.ToolPolicy,
		baseAuth:   cfg.Auth,
		appName:    cfg.Name,
		store:      store,
		prompter:   prompter,
		operations: append([]OperationDescriptor(nil), operations...),
		runtimes:   map[string]*apiProfileRuntime{},
	}
	registry, err := manager.loadOrCreateRegistry()
	if err != nil {
		return nil, err
	}
	manager.registry = registry
	for _, profile := range registry.Profiles {
		runtime, err := manager.newRuntime(profile)
		if err != nil {
			return nil, fmt.Errorf("initialize profile %q: %w", profile.ID, err)
		}
		manager.runtimes[profile.ID] = runtime
	}
	return manager, nil
}

func (m *profileManager) loadOrCreateRegistry() (ProfileRegistry, error) {
	data, err := os.ReadFile(m.config.RegistryPath)
	if errors.Is(err, os.ErrNotExist) {
		registry := ProfileRegistry{
			Version: profileRegistryVersion,
			Profiles: []APIProfile{{
				ID:   m.config.DefaultProfileID,
				Name: m.config.DefaultProfileName,
			}},
		}
		if err := m.validateRegistry(registry); err != nil {
			return ProfileRegistry{}, err
		}
		if err := writeJSONFileAtomically(m.config.RegistryPath, registry, true); err != nil {
			if errors.Is(err, os.ErrExist) {
				return m.loadOrCreateRegistry()
			}
			return ProfileRegistry{}, fmt.Errorf("create default profile registry: %w", err)
		}
		return registry, nil
	}
	if err != nil {
		return ProfileRegistry{}, fmt.Errorf("read profile registry: %w", err)
	}
	if err := os.Chmod(filepath.Dir(m.config.RegistryPath), 0o700); err != nil {
		return ProfileRegistry{}, fmt.Errorf("protect profile registry directory: %w", err)
	}
	var registry ProfileRegistry
	if err := decodeStrictJSON(data, &registry); err != nil {
		return ProfileRegistry{}, fmt.Errorf("parse profile registry: %w", err)
	}
	registry = normalizeProfileRegistry(registry)
	if err := m.validateRegistry(registry); err != nil {
		return ProfileRegistry{}, fmt.Errorf("invalid profile registry: %w", err)
	}
	if err := os.Chmod(m.config.RegistryPath, 0o600); err != nil {
		return ProfileRegistry{}, fmt.Errorf("protect profile registry: %w", err)
	}
	return registry, nil
}

func (m *profileManager) validateRegistry(registry ProfileRegistry) error {
	if registry.Version != profileRegistryVersion {
		return fmt.Errorf("unsupported profile registry version %d", registry.Version)
	}
	if len(registry.Profiles) == 0 {
		return errors.New("profile registry must contain at least one profile")
	}
	seen := map[string]bool{}
	for _, profile := range registry.Profiles {
		if err := validateProfileID(profile.ID); err != nil {
			return err
		}
		if err := validateProfileName(profile.Name); err != nil {
			return fmt.Errorf("profile %q: %w", profile.ID, err)
		}
		if seen[profile.ID] {
			return fmt.Errorf("duplicate profile ID %q", profile.ID)
		}
		seen[profile.ID] = true
	}
	return nil
}

func (m *profileManager) newRuntime(profile APIProfile) (*apiProfileRuntime, error) {
	policyConfig := m.basePolicy
	if profile.ID != m.config.DefaultProfileID {
		policyConfig.UserConfigPath = filepath.Join(m.config.ProfilesDirectory, profile.ID, "tools.json")
	}
	policy, err := newToolPolicyManager(policyConfig, m.operations)
	if err != nil {
		return nil, err
	}
	auth := m.authForProfile(profile.ID)
	if profile.ID != m.config.DefaultProfileID && auth.Keychain.Account == m.baseAuth.Keychain.Account {
		return nil, fmt.Errorf("derived Keychain account for profile %q conflicts with the default profile", profile.ID)
	}
	credentials := newCredentialManager(m.applicationName(profile), auth, m.store, m.prompter)
	return &apiProfileRuntime{
		profile:     profile,
		policy:      policy,
		credentials: credentials,
		keychain:    *auth.Keychain,
	}, nil
}

func (m *profileManager) authForProfile(profileID string) ResolvedAuthConfig {
	auth := m.baseAuth
	auth.Keychain = cloneKeychainConfig(m.baseAuth.Keychain)
	if profileID == m.config.DefaultProfileID {
		return auth
	}
	auth.Token = ""
	auth.Password = ""
	auth.Value = ""
	auth.CredentialMissing = true
	auth.Keychain.Account = "profile:" + profileID
	return auth
}

func (m *profileManager) applicationName(profile APIProfile) string {
	return m.appName + " — " + profile.Name
}

func (m *profileManager) resolve(profileID string) (*apiProfileRuntime, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		if len(m.runtimes) != 1 {
			return nil, errors.New("profile is required because multiple API accounts are configured; call list_api_profiles")
		}
		for _, runtime := range m.runtimes {
			return runtime, nil
		}
	}
	runtime, ok := m.runtimes[profileID]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q; call list_api_profiles", profileID)
	}
	return runtime, nil
}

func (m *profileManager) list() ([]APIProfileStatus, error) {
	m.mu.Lock()
	runtimes := make([]*apiProfileRuntime, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtimes = append(runtimes, runtime)
	}
	m.mu.Unlock()

	result := make([]APIProfileStatus, 0, len(runtimes))
	for _, runtime := range runtimes {
		status, err := runtime.status()
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", runtime.profile.ID, err)
		}
		result = append(result, status)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func (r *apiProfileRuntime) status() (APIProfileStatus, error) {
	available, stored, err := r.credentials.statusDetails()
	if err != nil {
		return APIProfileStatus{}, err
	}
	_, path := r.policy.snapshot()
	return APIProfileStatus{
		ID:                         r.profile.ID,
		Name:                       r.profile.Name,
		CredentialAvailable:        available,
		CredentialStoredInKeychain: stored,
		ToolConfigPath:             path,
		EnabledEndpointCount:       r.policy.enabledEndpointCount(),
	}, nil
}

func (m *profileManager) add(ctx context.Context, profile APIProfile) (APIProfileStatus, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	profile.Name = strings.TrimSpace(profile.Name)
	if err := validateProfileID(profile.ID); err != nil {
		return APIProfileStatus{}, err
	}
	if err := validateProfileName(profile.Name); err != nil {
		return APIProfileStatus{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.runtimes[profile.ID]; exists {
		return APIProfileStatus{}, fmt.Errorf("profile %q already exists", profile.ID)
	}
	previous := cloneProfileRegistry(m.registry)
	next := cloneProfileRegistry(m.registry)
	next.Profiles = append(next.Profiles, profile)
	next = normalizeProfileRegistry(next)
	if err := writeJSONFileAtomically(m.config.RegistryPath, next, false); err != nil {
		return APIProfileStatus{}, fmt.Errorf("save profile registry: %w", err)
	}

	policyPath := filepath.Join(m.config.ProfilesDirectory, profile.ID, "tools.json")
	_, statErr := os.Stat(policyPath)
	policyExisted := statErr == nil
	runtime, err := m.newRuntime(profile)
	if err == nil {
		err = runtime.credentials.setup(ctx)
	}
	if err != nil {
		rollbackErr := writeJSONFileAtomically(m.config.RegistryPath, previous, false)
		if !policyExisted {
			_ = os.Remove(policyPath)
			_ = os.Remove(filepath.Dir(policyPath))
		}
		if rollbackErr != nil {
			return APIProfileStatus{}, fmt.Errorf("add profile failed: %v; rollback profile registry: %w", err, rollbackErr)
		}
		return APIProfileStatus{}, err
	}
	m.registry = next
	m.runtimes[profile.ID] = runtime
	return runtime.status()
}

func (m *profileManager) rename(profileID, name string) (APIProfile, error) {
	profileID = strings.TrimSpace(profileID)
	name = strings.TrimSpace(name)
	if err := validateProfileName(name); err != nil {
		return APIProfile{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.runtimes[profileID]
	if !ok {
		return APIProfile{}, fmt.Errorf("unknown profile %q", profileID)
	}
	next := cloneProfileRegistry(m.registry)
	for i := range next.Profiles {
		if next.Profiles[i].ID == profileID {
			next.Profiles[i].Name = name
			break
		}
	}
	if err := writeJSONFileAtomically(m.config.RegistryPath, next, false); err != nil {
		return APIProfile{}, fmt.Errorf("save profile registry: %w", err)
	}
	profile := runtime.profile
	profile.Name = name
	nextRuntime := *runtime
	nextRuntime.profile = profile
	runtime.credentials.setApplicationName(m.applicationName(profile))
	m.registry = next
	m.runtimes[profileID] = &nextRuntime
	return profile, nil
}

func (m *profileManager) remove(profileID string, deleteCredential, deleteToolConfiguration bool) (APIProfileRemoval, error) {
	profileID = strings.TrimSpace(profileID)
	m.mu.Lock()
	defer m.mu.Unlock()
	runtime, ok := m.runtimes[profileID]
	if !ok {
		return APIProfileRemoval{}, fmt.Errorf("unknown profile %q", profileID)
	}
	if len(m.runtimes) == 1 {
		return APIProfileRemoval{}, errors.New("cannot remove the last API profile")
	}
	next := cloneProfileRegistry(m.registry)
	next.Profiles = removeProfile(next.Profiles, profileID)
	if err := writeJSONFileAtomically(m.config.RegistryPath, next, false); err != nil {
		return APIProfileRemoval{}, fmt.Errorf("save profile registry: %w", err)
	}
	m.registry = next
	delete(m.runtimes, profileID)

	result := APIProfileRemoval{Profile: runtime.profile, Warnings: []string{}}
	if deleteCredential {
		if err := m.store.Delete(runtime.keychain); err != nil {
			result.Warnings = append(result.Warnings, "profile was removed, but its Keychain credential could not be deleted: "+err.Error())
		} else {
			result.CredentialDeleted = true
		}
	}
	if deleteToolConfiguration {
		_, path := runtime.policy.snapshot()
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result.Warnings = append(result.Warnings, "profile was removed, but its tool configuration could not be deleted: "+err.Error())
		} else {
			result.ToolConfigurationDeleted = true
			if profileID != m.config.DefaultProfileID {
				_ = os.Remove(filepath.Dir(path))
			}
		}
	}
	return result, nil
}

func (m *profileManager) allowsAny(operation OperationDescriptor) bool {
	m.mu.Lock()
	runtimes := make([]*apiProfileRuntime, 0, len(m.runtimes))
	for _, runtime := range m.runtimes {
		runtimes = append(runtimes, runtime)
	}
	m.mu.Unlock()
	for _, runtime := range runtimes {
		if ShouldExposeOperation(operation, runtime.policy.filter()) {
			return true
		}
	}
	return false
}

func (m *profileManager) registryPath() string {
	return m.config.RegistryPath
}

func validateProfileID(id string) error {
	if !profileIDPattern.MatchString(id) {
		return errors.New("profile ID must use 1-63 lowercase letters, digits, or hyphens and start with a letter or digit")
	}
	return nil
}

func validateProfileName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("profile name is required")
	}
	if len(name) > 100 {
		return errors.New("profile name must be at most 100 characters")
	}
	for _, character := range name {
		if unicode.IsControl(character) {
			return errors.New("profile name must not contain control characters")
		}
	}
	return nil
}

func normalizeProfileRegistry(registry ProfileRegistry) ProfileRegistry {
	for i := range registry.Profiles {
		registry.Profiles[i].ID = strings.TrimSpace(registry.Profiles[i].ID)
		registry.Profiles[i].Name = strings.TrimSpace(registry.Profiles[i].Name)
	}
	sort.Slice(registry.Profiles, func(i, j int) bool { return registry.Profiles[i].ID < registry.Profiles[j].ID })
	return registry
}

func cloneProfileRegistry(registry ProfileRegistry) ProfileRegistry {
	registry.Profiles = append([]APIProfile{}, registry.Profiles...)
	return registry
}

func removeProfile(profiles []APIProfile, profileID string) []APIProfile {
	result := make([]APIProfile, 0, len(profiles)-1)
	for _, profile := range profiles {
		if profile.ID != profileID {
			result = append(result, profile)
		}
	}
	return result
}
