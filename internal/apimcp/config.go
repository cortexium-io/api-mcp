package apimcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type LookupEnv func(string) (string, bool)

type AuthConfig struct {
	Type        string            `json:"type,omitempty"`
	Token       string            `json:"token,omitempty"`
	TokenEnv    string            `json:"tokenEnv,omitempty"`
	Username    string            `json:"username,omitempty"`
	UsernameEnv string            `json:"usernameEnv,omitempty"`
	Password    string            `json:"password,omitempty"`
	PasswordEnv string            `json:"passwordEnv,omitempty"`
	In          string            `json:"in,omitempty"`
	Name        string            `json:"name,omitempty"`
	Value       string            `json:"value,omitempty"`
	ValueEnv    string            `json:"valueEnv,omitempty"`
	Keychain    *KeychainConfig   `json:"keychain,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

type ResolvedAuthConfig struct {
	Type              string
	Token             string
	Username          string
	Password          string
	In                string
	Name              string
	Value             string
	Headers           map[string]string
	RedactionValues   []string
	Keychain          *KeychainConfig
	CredentialMissing bool
}

type ToolFilterConfig struct {
	Include       []string `json:"include,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	DefaultPolicy string   `json:"defaultPolicy,omitempty"`
}

type ToolPolicyConfig struct {
	UserConfigDirectory string   `json:"userConfigDirectory"`
	UserConfigFile      string   `json:"userConfigFile,omitempty"`
	Catalog             string   `json:"catalog"`
	DefaultFeatures     []string `json:"defaultFeatures"`
}

type ProfilesConfig struct {
	RegistryFile       string `json:"registryFile,omitempty"`
	DefaultProfileID   string `json:"defaultProfileId,omitempty"`
	DefaultProfileName string `json:"defaultProfileName,omitempty"`
}

type ResolvedProfilesConfig struct {
	RegistryPath       string
	ProfilesDirectory  string
	DefaultProfileID   string
	DefaultProfileName string
}

type ConfigInput struct {
	Name              string            `json:"name,omitempty"`
	Version           string            `json:"version,omitempty"`
	Spec              string            `json:"spec"`
	AllowRemoteSpec   bool              `json:"allowRemoteSpec,omitempty"`
	AllowExternalRefs bool              `json:"allowExternalRefs,omitempty"`
	ServerURL         string            `json:"serverUrl,omitempty"`
	Auth              *AuthConfig       `json:"auth,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	Tools             ToolFilterConfig  `json:"tools,omitempty"`
	ToolPolicy        *ToolPolicyConfig `json:"toolPolicy,omitempty"`
	Profiles          *ProfilesConfig   `json:"profiles,omitempty"`
	MaxResponseChars  int               `json:"maxResponseChars,omitempty"`
}

type Config struct {
	Name              string
	Version           string
	Spec              string
	AllowRemoteSpec   bool
	AllowExternalRefs bool
	ServerURL         string
	Auth              ResolvedAuthConfig
	Headers           map[string]string
	Tools             ToolFilterConfig
	ToolPolicy        *ResolvedToolPolicyConfig
	Profiles          *ResolvedProfilesConfig
	MaxResponseChars  int
}

var (
	DefaultVersion       = "dev"
	EmbeddedConfigBase64 string
)

func LoadConfigFromArgs(args []string) (Config, error) {
	input, baseDir, err := loadConfigInputFromArgs(args, os.LookupEnv)
	if err != nil {
		return Config{}, err
	}
	return NormalizeConfig(input, baseDir, os.LookupEnv)
}

func LoadConfigFile(configPath string, lookup LookupEnv) (Config, error) {
	input, baseDir, err := loadConfigInputFile(configPath)
	if err != nil {
		return Config{}, err
	}
	return NormalizeConfig(input, baseDir, lookup)
}

func loadConfigInputFromArgs(args []string, lookup LookupEnv) (ConfigInput, string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return ConfigInput{}, "", err
	}

	configPath, found, err := findConfigPath(args, lookup)
	if err != nil {
		return ConfigInput{}, "", err
	}
	if found {
		return loadConfigInputFile(configPath)
	}

	if EmbeddedConfigBase64 != "" {
		input, err := loadEmbeddedConfigInput()
		return input, "", err
	}

	defaultPath := filepath.Join(cwd, "api-mcp.config.json")
	if _, err := os.Stat(defaultPath); err == nil {
		return loadConfigInputFile(defaultPath)
	}

	return ConfigInput{}, "", errors.New("no config file found; pass --config path/to/config.json, set API_MCP_CONFIG, or use a binary with embedded config")
}

func loadConfigInputFile(configPath string) (ConfigInput, string, error) {
	absolutePath, err := filepath.Abs(configPath)
	if err != nil {
		return ConfigInput{}, "", err
	}

	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return ConfigInput{}, "", err
	}

	input, err := decodeConfigInput(data)
	if err != nil {
		return ConfigInput{}, "", err
	}
	return input, filepath.Dir(absolutePath), nil
}

func LoadEmbeddedConfig(lookup LookupEnv) (Config, error) {
	input, err := loadEmbeddedConfigInput()
	if err != nil {
		return Config{}, err
	}
	return NormalizeConfig(input, "", lookup)
}

func loadEmbeddedConfigInput() (ConfigInput, error) {
	if EmbeddedConfigBase64 == "" {
		return ConfigInput{}, errors.New("no embedded config is available")
	}
	data, err := base64.StdEncoding.DecodeString(EmbeddedConfigBase64)
	if err != nil {
		return ConfigInput{}, fmt.Errorf("decode embedded config: %w", err)
	}
	input, err := decodeConfigInput(data)
	if err != nil {
		return ConfigInput{}, fmt.Errorf("parse embedded config: %w", err)
	}
	return input, nil
}

func EmbeddedConfigForFile(configPath string) (string, error) {
	absolutePath, err := filepath.Abs(configPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return "", err
	}

	input, err := decodeConfigInput(data)
	if err != nil {
		return "", err
	}
	if input.Spec == "" {
		return "", errors.New("config must include a string `spec` path or URL")
	}
	if !isHTTPURL(input.Spec) {
		input.Spec = filepath.Join(filepath.Dir(absolutePath), input.Spec)
	}

	compact, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(compact), nil
}

func decodeConfigInput(data []byte) (ConfigInput, error) {
	var input ConfigInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return ConfigInput{}, err
	}
	// Like json.Unmarshal, require exactly one JSON value.
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return ConfigInput{}, err
		}
		return ConfigInput{}, errors.New("config must contain a single JSON value")
	}
	return input, nil
}

func NormalizeConfig(input ConfigInput, baseDir string, lookup LookupEnv) (Config, error) {
	return normalizeConfig(input, baseDir, lookup, platformKeychainStore())
}

func normalizeConfig(input ConfigInput, baseDir string, lookup LookupEnv, keychainStore KeychainStore) (Config, error) {
	if input.Spec == "" {
		return Config{}, errors.New("config must include a string `spec` path or URL")
	}

	spec := input.Spec
	if isHTTPURL(spec) {
		if !input.AllowRemoteSpec {
			return Config{}, errors.New("remote OpenAPI documents are disabled by default; set `allowRemoteSpec` to true to opt in")
		}
		if !strings.HasPrefix(strings.ToLower(spec), "https://") {
			return Config{}, errors.New("remote OpenAPI documents must use HTTPS")
		}
	} else {
		spec = filepath.Join(baseDir, spec)
	}

	name := input.Name
	if name == "" {
		name = "api-mcp"
	}

	version := input.Version
	if version == "" {
		version = DefaultVersion
	}

	maxResponseChars := input.MaxResponseChars
	if maxResponseChars == 0 {
		maxResponseChars = 20_000
	}

	headers, err := resolveHeaderValues(input.Headers, lookup)
	if err != nil {
		return Config{}, err
	}

	auth, err := resolveAuth(input.Auth, lookup, keychainStore)
	if err != nil {
		return Config{}, err
	}
	if auth.Type != "none" && input.ServerURL == "" && (isHTTPURL(input.Spec) || input.AllowExternalRefs) {
		return Config{}, errors.New("authenticated configurations with a remote OpenAPI document or external references must set `serverUrl` explicitly")
	}
	tools, err := normalizeToolFilter(input.Tools)
	if err != nil {
		return Config{}, err
	}
	toolPolicy, err := resolveToolPolicyConfig(input.ToolPolicy, baseDir)
	if err != nil {
		return Config{}, err
	}
	if toolPolicy != nil && (len(tools.Include) > 0 || len(tools.Exclude) > 0 || tools.DefaultPolicy != "") {
		return Config{}, errors.New("configure either `tools` or `toolPolicy`, not both")
	}
	profiles, err := resolveProfilesConfig(input.Profiles, toolPolicy, auth)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Name:              name,
		Version:           version,
		Spec:              spec,
		AllowRemoteSpec:   input.AllowRemoteSpec,
		AllowExternalRefs: input.AllowExternalRefs,
		ServerURL:         input.ServerURL,
		Auth:              auth,
		Headers:           headers,
		Tools:             tools,
		ToolPolicy:        toolPolicy,
		Profiles:          profiles,
		MaxResponseChars:  maxResponseChars,
	}, nil
}

func findConfigPath(args []string, lookup LookupEnv) (string, bool, error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--config" {
			if i+1 >= len(args) || args[i+1] == "" {
				return "", false, errors.New("missing value after --config")
			}
			if i+2 < len(args) {
				return "", false, fmt.Errorf("unexpected argument: %s", args[i+2])
			}
			return args[i+1], true, nil
		}
		if strings.HasPrefix(arg, "--config=") {
			configPath := strings.TrimPrefix(arg, "--config=")
			if configPath == "" {
				return "", false, errors.New("missing value after --config")
			}
			if i+1 < len(args) {
				return "", false, fmt.Errorf("unexpected argument: %s", args[i+1])
			}
			return configPath, true, nil
		}
		if strings.HasPrefix(arg, "-") {
			return "", false, fmt.Errorf("unknown option: %s", arg)
		}
		return "", false, fmt.Errorf("unexpected argument: %s", arg)
	}

	if value, ok := lookup("API_MCP_CONFIG"); ok && value != "" {
		return value, true, nil
	}

	return "", false, nil
}

func resolveAuth(auth *AuthConfig, lookup LookupEnv, keychainStore KeychainStore) (ResolvedAuthConfig, error) {
	if auth == nil || auth.Type == "" || auth.Type == "none" {
		if auth != nil && auth.Keychain != nil {
			return ResolvedAuthConfig{}, errors.New("keychain is not supported for auth type `none`")
		}
		return ResolvedAuthConfig{Type: "none"}, nil
	}

	switch auth.Type {
	case "bearer":
		if shouldDeferKeychainCredential(auth.Token, auth.TokenEnv, auth.Keychain, lookup) {
			return ResolvedAuthConfig{Type: "bearer", Keychain: cloneKeychainConfig(auth.Keychain), CredentialMissing: true}, nil
		}
		token, err := requiredSecret("bearer token", auth.Token, auth.TokenEnv, auth.Keychain, lookup, keychainStore)
		if err != nil {
			return ResolvedAuthConfig{}, err
		}
		return ResolvedAuthConfig{Type: "bearer", Token: token, Keychain: cloneKeychainConfig(auth.Keychain)}, nil
	case "basic":
		username, err := requiredSecret("basic username", auth.Username, auth.UsernameEnv, nil, lookup, keychainStore)
		if err != nil {
			return ResolvedAuthConfig{}, err
		}
		if shouldDeferKeychainCredential(auth.Password, auth.PasswordEnv, auth.Keychain, lookup) {
			return ResolvedAuthConfig{Type: "basic", Username: username, Keychain: cloneKeychainConfig(auth.Keychain), CredentialMissing: true}, nil
		}
		password, err := requiredSecret("basic password", auth.Password, auth.PasswordEnv, auth.Keychain, lookup, keychainStore)
		if err != nil {
			return ResolvedAuthConfig{}, err
		}
		return ResolvedAuthConfig{Type: "basic", Username: username, Password: password, Keychain: cloneKeychainConfig(auth.Keychain)}, nil
	case "apiKey":
		if auth.In != "headers" && auth.In != "query" && auth.In != "cookies" {
			return ResolvedAuthConfig{}, errors.New("apiKey auth requires `in` to be `headers`, `query`, or `cookies`")
		}
		if auth.Name == "" {
			return ResolvedAuthConfig{}, errors.New("apiKey auth requires `name`")
		}
		if shouldDeferKeychainCredential(auth.Value, auth.ValueEnv, auth.Keychain, lookup) {
			return ResolvedAuthConfig{Type: "apiKey", In: auth.In, Name: auth.Name, Keychain: cloneKeychainConfig(auth.Keychain), CredentialMissing: true}, nil
		}
		value, err := requiredSecret("api key", auth.Value, auth.ValueEnv, auth.Keychain, lookup, keychainStore)
		if err != nil {
			return ResolvedAuthConfig{}, err
		}
		return ResolvedAuthConfig{Type: "apiKey", In: auth.In, Name: auth.Name, Value: value, Keychain: cloneKeychainConfig(auth.Keychain)}, nil
	case "headers":
		if auth.Keychain != nil {
			return ResolvedAuthConfig{}, errors.New("keychain is not supported for auth type `headers`; use environment placeholders")
		}
		headers, redactionValues, err := resolveHeaderValuesWithSecrets(auth.Headers, lookup)
		if err != nil {
			return ResolvedAuthConfig{}, err
		}
		return ResolvedAuthConfig{Type: "headers", Headers: headers, RedactionValues: redactionValues}, nil
	default:
		return ResolvedAuthConfig{}, fmt.Errorf("unsupported auth type: %s", auth.Type)
	}
}

func shouldDeferKeychainCredential(inlineValue, envName string, ref *KeychainConfig, lookup LookupEnv) bool {
	if ref == nil || !ref.PromptOnMissing || inlineValue != "" {
		return false
	}
	if envName != "" {
		if value, ok := lookup(envName); ok && value != "" {
			return false
		}
	}
	return true
}

func cloneKeychainConfig(ref *KeychainConfig) *KeychainConfig {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func requiredSecret(label, inlineValue, envName string, keychainRef *KeychainConfig, lookup LookupEnv, keychainStore KeychainStore) (string, error) {
	if inlineValue != "" {
		return resolveEnvPlaceholders(inlineValue, lookup)
	}
	if envName != "" {
		if value, ok := lookup(envName); ok && value != "" {
			return value, nil
		}
		if keychainRef == nil {
			return "", fmt.Errorf("environment variable %s is required for %s", envName, label)
		}
	}
	if keychainRef != nil {
		return keychainSecret(label, keychainRef, keychainStore)
	}
	return "", fmt.Errorf("missing %s; provide an inline value, env var name, or keychain source", label)
}

func resolveHeaderValues(headers map[string]string, lookup LookupEnv) (map[string]string, error) {
	resolved, _, err := resolveHeaderValuesWithSecrets(headers, lookup)
	return resolved, err
}

func resolveHeaderValuesWithSecrets(headers map[string]string, lookup LookupEnv) (map[string]string, []string, error) {
	resolved := map[string]string{}
	var secrets []string
	for name, value := range headers {
		next, values, err := resolveEnvPlaceholdersWithSecrets(value, lookup)
		if err != nil {
			return nil, nil, err
		}
		resolved[name] = next
		for _, secret := range values {
			if !slices.Contains(secrets, secret) {
				secrets = append(secrets, secret)
			}
		}
	}
	return resolved, secrets, nil
}

var envPlaceholderPattern = regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)

func resolveEnvPlaceholders(value string, lookup LookupEnv) (string, error) {
	result, _, err := resolveEnvPlaceholdersWithSecrets(value, lookup)
	return result, err
}

func resolveEnvPlaceholdersWithSecrets(value string, lookup LookupEnv) (string, []string, error) {
	var resultErr error
	var secrets []string
	result := envPlaceholderPattern.ReplaceAllStringFunc(value, func(match string) string {
		if resultErr != nil {
			return ""
		}
		name := envPlaceholderPattern.FindStringSubmatch(match)[1]
		envValue, ok := lookup(name)
		if !ok || envValue == "" {
			resultErr = fmt.Errorf("environment variable %s is required", name)
			return ""
		}
		if !slices.Contains(secrets, envValue) {
			secrets = append(secrets, envValue)
		}
		return envValue
	})
	if resultErr != nil {
		return "", nil, resultErr
	}
	return result, secrets, nil
}

func normalizeToolFilter(filter ToolFilterConfig) (ToolFilterConfig, error) {
	defaultPolicy := strings.ToLower(strings.TrimSpace(filter.DefaultPolicy))
	if defaultPolicy != "" && defaultPolicy != "allow" && defaultPolicy != "deny" {
		return ToolFilterConfig{}, errors.New("tools.defaultPolicy must be `allow` or `deny`")
	}
	return ToolFilterConfig{
		Include:       normalizeStringList(filter.Include),
		Exclude:       normalizeStringList(filter.Exclude),
		DefaultPolicy: defaultPolicy,
	}, nil
}

func normalizeStringList(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func isHTTPURL(value string) bool {
	return strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://")
}
