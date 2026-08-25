package apimcp

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type profileSelectorInput struct {
	Profile string `json:"profile,omitempty"`
}

type addProfileInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type renameProfileInput struct {
	Profile string `json:"profile"`
	Name    string `json:"name"`
}

type removeProfileInput struct {
	Profile                 string `json:"profile"`
	DeleteCredential        bool   `json:"deleteCredential,omitempty"`
	DeleteToolConfiguration bool   `json:"deleteToolConfiguration,omitempty"`
}

type profileSearchAvailableToolsInput struct {
	Profile     string `json:"profile,omitempty"`
	Query       string `json:"query,omitempty"`
	EnabledOnly *bool  `json:"enabledOnly,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type profileConfigureToolsInput struct {
	Profile         string   `json:"profile,omitempty"`
	AddFeatures     []string `json:"addFeatures,omitempty"`
	RemoveFeatures  []string `json:"removeFeatures,omitempty"`
	AddEndpoints    []string `json:"addEndpoints,omitempty"`
	RemoveEndpoints []string `json:"removeEndpoints,omitempty"`
}

func addProfileTools(server *mcp.Server, profiles *profileManager, registry *operationToolRegistry) {
	closedWorld := false
	destructive := true
	nonDestructive := false
	var mutationMu sync.Mutex

	server.AddTool(&mcp.Tool{
		Name:        listProfilesToolName,
		Title:       "List API account profiles",
		Description: "List the configured local API account profiles, credential availability, per-profile tool configuration paths, and enabled endpoint counts. Credential values are never returned.",
		InputSchema: strictEmptyObjectSchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireNoToolArguments(req); err != nil {
			return toolError(err.Error()), nil
		}
		items, err := profiles.list()
		if err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Configured API account profiles.", map[string]any{
			"registryPath": profiles.registryPath(),
			"profiles":     items,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        addProfileToolName,
		Title:       "Add API account profile",
		Description: "Add a local API account profile, then open the native secure-input dialog to store that account's credential directly in Keychain. A new ID starts with the configured default policy; reusing a removed ID restores its preserved policy file if it was not deleted. Before calling, explain what will be created or restored and obtain explicit user confirmation. Never accept the credential as an argument.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":   map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,62}$", "description": "Stable lowercase profile ID, such as `company-a`."},
				"name": map[string]any{"type": "string", "minLength": 1, "maxLength": 100, "description": "Friendly account name shown in tool results and secure credential prompts."},
			},
			"required":             []string{"id", "name"},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		var input addProfileInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		status, err := profiles.add(ctx, APIProfile{ID: input.ID, Name: input.Name})
		if err != nil {
			return toolError(err.Error()), nil
		}
		registry.applyProfiles()
		return jsonToolResponse("Added the API account profile with a separate tool policy and Keychain credential. Review the returned enabled endpoint count when reusing a previously removed profile ID.", map[string]any{"profile": status}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        renameProfileToolName,
		Title:       "Rename API account profile",
		Description: "Change a profile's friendly display name. Its stable ID, Keychain credential, and tool policy remain unchanged.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"profile": profileIDSchema("Profile ID to rename."),
				"name":    map[string]any{"type": "string", "minLength": 1, "maxLength": 100, "description": "New friendly display name."},
			},
			"required":             []string{"profile", "name"},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		var input renameProfileInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		profile, err := profiles.rename(input.Profile, input.Name)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Renamed the API account profile.", map[string]any{"profile": profile}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        removeProfileToolName,
		Title:       "Remove API account profile",
		Description: "Remove a local API account profile. Before calling, explain whether its Keychain credential and tool-policy file will also be deleted and obtain explicit user confirmation. At least one profile must remain. Omitted deletion options preserve the credential and policy for recovery.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"profile":                 profileIDSchema("Profile ID to remove."),
				"deleteCredential":        map[string]any{"type": "boolean", "description": "Also permanently delete this profile's Keychain credential. Defaults to false."},
				"deleteToolConfiguration": map[string]any{"type": "boolean", "description": "Also delete this profile's local tool-policy file. Defaults to false."},
			},
			"required":             []string{"profile"},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		var input removeProfileInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		removed, err := profiles.remove(input.Profile, input.DeleteCredential, input.DeleteToolConfiguration)
		if err != nil {
			return toolError(err.Error()), nil
		}
		registry.applyProfiles()
		return jsonToolResponse("Removed the API account profile.", map[string]any{"removal": removed}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        credentialStatusToolName,
		Title:       "Check profile credential",
		Description: "Check whether the selected API account profile has an available credential and whether one is stored in macOS Keychain. This never returns credential material.",
		InputSchema: profileSelectorSchema("Account profile to check. Optional when exactly one profile exists."),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input profileSelectorInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		status, err := runtime.status()
		if err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Credential status for the selected API account profile. No credential material was read into the conversation.", map[string]any{"profile": status}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        credentialSetupToolName,
		Title:       "Set up profile credential",
		Description: "Open a native macOS secure-input dialog and store or replace the selected account profile's API credential directly in Keychain. The credential is never accepted as a tool argument or returned to the client.",
		InputSchema: profileSelectorSchema("Account profile whose credential should be stored or replaced. Optional when exactly one profile exists."),
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		var input profileSelectorInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		if err := runtime.credentials.setup(ctx); err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Stored the selected profile's credential in macOS Keychain. The credential was not returned to the client.", map[string]any{
			"profile": map[string]any{"id": runtime.profile.ID, "name": runtime.profile.Name, "credentialStoredInKeychain": true},
		}), nil
	})

	addProfileToolPolicyTools(server, profiles, registry, &mutationMu, closedWorld, destructive)
}

func addProfileToolPolicyTools(server *mcp.Server, profiles *profileManager, registry *operationToolRegistry, mutationMu *sync.Mutex, closedWorld, destructive bool) {
	server.AddTool(&mcp.Tool{
		Name:        toolConfigurationToolName,
		Title:       "Get profile tool configuration",
		Description: "Show the selected account profile's local tool configuration, file location, and enabled endpoint count. This does not expose credentials.",
		InputSchema: profileSelectorSchema("Account profile to inspect. Optional when exactly one profile exists."),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input profileSelectorInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		current, path := runtime.policy.snapshot()
		return jsonToolResponse("Current tool configuration for the selected account profile.", map[string]any{
			"profile":              runtime.profile,
			"configPath":           path,
			"configuration":        current,
			"enabledEndpointCount": runtime.policy.enabledEndpointCount(),
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        toolFeaturesToolName,
		Title:       "List profile tool features",
		Description: "Explain named API feature groups and show whether each is enabled for the selected account profile.",
		InputSchema: profileSelectorSchema("Account profile whose feature state should be shown. Optional when exactly one profile exists."),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input profileSelectorInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Available feature groups for the selected account profile.", map[string]any{
			"profile":  runtime.profile,
			"features": runtime.policy.features(),
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        toolSearchToolName,
		Title:       "Search profile tools",
		Description: "Search and explain API endpoints for the selected account profile, including whether each endpoint is enabled there.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"profile":     profileIDSchema("Account profile to inspect. Optional when exactly one profile exists."),
				"query":       map[string]any{"type": "string", "description": "Optional text matched against tool name, title, description, HTTP method, and path."},
				"enabledOnly": map[string]any{"type": "boolean", "description": "When true, return only enabled endpoints; when false, only disabled endpoints. Omit for both."},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Maximum results. Defaults to 50."},
			},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input profileSearchAvailableToolsInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		endpoints := runtime.policy.endpoints(input.Query, input.EnabledOnly, input.Limit)
		return jsonToolResponse("API endpoint explanations for the selected account profile.", map[string]any{
			"profile":   runtime.profile,
			"count":     len(endpoints),
			"endpoints": endpoints,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        configureToolsToolName,
		Title:       "Configure profile tools",
		Description: "Update tool availability for one account profile by enabling or disabling named features and endpoint selectors. Before calling, identify the exact profile, explain the exact access change, call out any write capability, and obtain explicit user confirmation. Changes take effect immediately.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"profile":         profileIDSchema("Account profile to configure. Optional when exactly one profile exists."),
				"addFeatures":     stringArraySchema("Named feature IDs to enable for this profile."),
				"removeFeatures":  stringArraySchema("Named feature IDs to disable for this profile."),
				"addEndpoints":    stringArraySchema("Endpoint selectors to enable for this profile."),
				"removeEndpoints": stringArraySchema("Endpoint selectors to disable for this profile. Explicit blocks override enabled features."),
			},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		mutationMu.Lock()
		defer mutationMu.Unlock()
		var input profileConfigureToolsInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		runtime, err := profiles.resolve(input.Profile)
		if err != nil {
			return toolError(err.Error()), nil
		}
		current, _, err := runtime.policy.configure(ConfigureToolsInput{
			AddFeatures:     input.AddFeatures,
			RemoveFeatures:  input.RemoveFeatures,
			AddEndpoints:    input.AddEndpoints,
			RemoveEndpoints: input.RemoveEndpoints,
		})
		if err != nil {
			return toolError(err.Error()), nil
		}
		registry.applyProfiles()
		_, path := runtime.policy.snapshot()
		return jsonToolResponse("Updated tool availability for the selected account profile and refreshed the MCP tool list.", map[string]any{
			"profile":              runtime.profile,
			"configPath":           path,
			"configuration":        current,
			"enabledEndpointCount": runtime.policy.enabledEndpointCount(),
		}), nil
	})
}

func profileSelectorSchema(description string) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"profile": profileIDSchema(description),
		},
		"additionalProperties": false,
	}
}

func profileIDSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"pattern":     "^[a-z0-9][a-z0-9-]{0,62}$",
		"description": description,
	}
}
