package apimcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	credentialStatusToolName  = "api_mcp_credential_status"
	credentialSetupToolName   = "api_mcp_setup_credential"
	toolConfigurationToolName = "get_tool_configuration"
	toolFeaturesToolName      = "list_tool_features"
	toolSearchToolName        = "search_available_tools"
	configureToolsToolName    = "configure_tools"
	listProfilesToolName      = "list_api_profiles"
	addProfileToolName        = "add_api_profile"
	renameProfileToolName     = "rename_api_profile"
	removeProfileToolName     = "remove_api_profile"
)

func CreateServer(ctx context.Context, cfg Config) (*mcp.Server, error) {
	return createServer(ctx, cfg, platformKeychainStore(), platformCredentialPrompter())
}

func createServer(ctx context.Context, cfg Config, store KeychainStore, prompter CredentialPrompter) (*mcp.Server, error) {
	operationConfig := cfg
	if cfg.ToolPolicy != nil {
		operationConfig.Tools = ToolFilterConfig{}
	}
	operations, err := LoadOperations(ctx, operationConfig)
	if err != nil {
		return nil, err
	}
	for _, operation := range operations {
		if isReservedToolName(operation.ToolName) {
			return nil, fmt.Errorf("OpenAPI operation conflicts with reserved tool name %q", operation.ToolName)
		}
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    cfg.Name,
		Version: cfg.Version,
	}, nil)
	var credentials *credentialManager
	var profiles *profileManager
	if cfg.Profiles != nil {
		profiles, err = newProfileManager(cfg, operations, store, prompter)
		if err != nil {
			return nil, err
		}
	} else {
		credentials = newCredentialManager(cfg.Name, cfg.Auth, store, prompter)
		if credentials.supportsSetup() {
			addCredentialTools(server, cfg.Auth.Keychain, credentials)
		}
	}
	registry := &operationToolRegistry{
		server:      server,
		config:      cfg,
		credentials: credentials,
		profiles:    profiles,
		operations:  operations,
	}
	if profiles != nil {
		addProfileTools(server, profiles, registry)
		registry.applyProfiles()
		return server, nil
	}
	filter := cfg.Tools
	if cfg.ToolPolicy != nil {
		policy, err := newToolPolicyManager(*cfg.ToolPolicy, operations)
		if err != nil {
			return nil, err
		}
		filter = policy.filter()
		addToolPolicyTools(server, policy, registry)
	}
	registry.apply(filter)

	return server, nil
}

type operationToolRegistry struct {
	server      *mcp.Server
	config      Config
	credentials *credentialManager
	profiles    *profileManager
	operations  []OperationDescriptor
}

func (r *operationToolRegistry) applyProfiles() {
	for _, operation := range r.operations {
		if r.profiles.allowsAny(operation) {
			r.add(operation)
		} else {
			r.server.RemoveTools(operation.ToolName)
		}
	}
}

func (r *operationToolRegistry) apply(filter ToolFilterConfig) {
	for _, operation := range r.operations {
		if ShouldExposeOperation(operation, filter) {
			r.add(operation)
		} else {
			r.server.RemoveTools(operation.ToolName)
		}
	}
}

func (r *operationToolRegistry) add(operation OperationDescriptor) {
	inputSchema := BuildToolInputSchema(operation)
	description := operation.Description
	if r.profiles != nil {
		inputSchema = addProfileToToolInputSchema(inputSchema)
		description += "\nAccount profile: pass `profile` when more than one API account is configured. Use list_api_profiles to discover stable profile IDs."
	}
	r.server.AddTool(&mcp.Tool{
		Name:        operation.ToolName,
		Title:       operation.Title,
		Description: description,
		InputSchema: inputSchema,
		Annotations: operationToolAnnotations(operation),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input ToolInput
		if req != nil && req.Params != nil && len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
				return toolError("invalid tool arguments: " + err.Error()), nil
			}
		}

		var auth ResolvedAuthConfig
		var selectedProfile *APIProfile
		if r.profiles != nil {
			runtime, err := r.profiles.resolve(input.Profile)
			if err != nil {
				return toolError(err.Error()), nil
			}
			if !ShouldExposeOperation(operation, runtime.policy.filter()) {
				return toolError(fmt.Sprintf("tool %q is disabled for profile %q; use search_available_tools or configure_tools for that profile", operation.ToolName, runtime.profile.ID)), nil
			}
			auth, err = runtime.credentials.authForRequest(ctx)
			if err != nil {
				return toolError(err.Error()), nil
			}
			profile := runtime.profile
			selectedProfile = &profile
		} else {
			var err error
			auth, err = r.credentials.authForRequest(ctx)
			if err != nil {
				return toolError(err.Error()), nil
			}
		}
		requestConfig := r.config
		requestConfig.Auth = auth
		response, err := ExecuteOperation(ctx, operation, input, requestConfig, nil)
		if err != nil {
			return toolError(err.Error()), nil
		}
		if selectedProfile != nil {
			response.Profile = selectedProfile
		}
		return toolResponse(response), nil
	})
}

func operationToolAnnotations(operation OperationDescriptor) *mcp.ToolAnnotations {
	openWorld := true
	if isReadOnlyMethod(operation.Method) {
		return &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: &openWorld,
		}
	}
	destructive := true
	return &mcp.ToolAnnotations{
		DestructiveHint: &destructive,
		OpenWorldHint:   &openWorld,
	}
}

func isReservedToolName(name string) bool {
	switch name {
	case credentialStatusToolName, credentialSetupToolName, toolConfigurationToolName, toolFeaturesToolName, toolSearchToolName, configureToolsToolName,
		listProfilesToolName, addProfileToolName, renameProfileToolName, removeProfileToolName:
		return true
	default:
		return false
	}
}

func addCredentialTools(server *mcp.Server, ref *KeychainConfig, credentials *credentialManager) {
	closedWorld := false
	nonDestructive := false
	emptySchema := strictEmptyObjectSchema()

	server.AddTool(&mcp.Tool{
		Name:        credentialStatusToolName,
		Title:       "Check API credential",
		Description: "Check whether the configured API credential is stored in macOS Keychain. This never returns the credential.",
		InputSchema: emptySchema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireNoToolArguments(req); err != nil {
			return toolError(err.Error()), nil
		}
		stored, err := credentials.status()
		if err != nil {
			return toolError(err.Error()), nil
		}
		message := "No API credential is stored in macOS Keychain. Call api_mcp_setup_credential to open secure setup."
		if stored {
			message = "An API credential is stored in macOS Keychain. The credential value was not read into the conversation."
		}
		return credentialToolResponse(stored, ref, message), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        credentialSetupToolName,
		Title:       "Set up API credential",
		Description: "Open a native macOS secure-input dialog and store or replace the configured API credential directly in Keychain. The credential is never accepted as a tool argument or returned to ChatGPT.",
		InputSchema: emptySchema,
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &nonDestructive, OpenWorldHint: &closedWorld},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireNoToolArguments(req); err != nil {
			return toolError(err.Error()), nil
		}
		if err := credentials.setup(ctx); err != nil {
			return toolError(err.Error()), nil
		}
		return credentialToolResponse(true, ref, "API credential stored in macOS Keychain. The credential value was not returned to ChatGPT."), nil
	})
}

func credentialToolResponse(stored bool, ref *KeychainConfig, message string) *mcp.CallToolResult {
	result := map[string]any{
		"stored":  stored,
		"service": ref.Service,
		"account": ref.Account,
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message + "\n" + string(data)}},
		StructuredContent: result,
	}
}

func RunStdio(ctx context.Context, cfg Config) error {
	server, err := CreateServer(ctx, cfg)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

func toolResponse(response APIResponse) *mcp.CallToolResult {
	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(data)}},
		StructuredContent: response,
		IsError:           !response.OK,
	}
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: message}},
		IsError: true,
	}
}
