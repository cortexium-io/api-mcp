package apimcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxExtensionBatchItems = 500

type ExtensionTool struct {
	Tool    *mcp.Tool
	Handler func(context.Context, *ExtensionRuntime, *mcp.CallToolRequest) (*mcp.CallToolResult, error)
}

type ExtensionBatchItem struct {
	ID        string    `json:"id"`
	Operation string    `json:"operation"`
	Input     ToolInput `json:"input"`
}

type ExtensionBatchRequest struct {
	Profile     string               `json:"profile,omitempty"`
	Action      string               `json:"action"`
	Details     any                  `json:"details,omitempty"`
	Items       []ExtensionBatchItem `json:"items"`
	StopOnError bool                 `json:"stopOnError"`
}

type ExtensionBatchResult struct {
	ID        string       `json:"id"`
	Operation string       `json:"operation"`
	Response  *APIResponse `json:"response,omitempty"`
	Error     string       `json:"error,omitempty"`
}

type ExtensionRuntime struct {
	config      Config
	credentials *credentialManager
	profiles    *profileManager
	policy      *toolPolicyManager
	filter      ToolFilterConfig
	operations  map[string]OperationDescriptor
	confirmer   ActionConfirmer
}

func (r *ExtensionRuntime) ResolveProfile(profileID string) (APIProfile, error) {
	if r.profiles == nil {
		if strings.TrimSpace(profileID) != "" {
			return APIProfile{}, errors.New("profile is not supported by this single-account MCP configuration")
		}
		return APIProfile{ID: "single", Name: "Single configured API account"}, nil
	}
	runtime, err := r.profiles.resolve(profileID)
	if err != nil {
		return APIProfile{}, err
	}
	return runtime.profile, nil
}

func (r *ExtensionRuntime) ExecuteRead(ctx context.Context, profileID, operationName string, input ToolInput) (APIResponse, error) {
	operation, err := r.validateEnabledOperation(profileID, operationName, input)
	if err != nil {
		return APIResponse{}, err
	}
	if !isReadOnlyMethod(operation.Method) {
		return APIResponse{}, fmt.Errorf("operation %q is not read-only", operation.ToolName)
	}
	return r.execute(ctx, profileID, operation, input)
}

func (r *ExtensionRuntime) executeGenerated(ctx context.Context, profileID, operationName string, input ToolInput, arguments map[string]any) (APIResponse, error) {
	operation, err := r.validateEnabledOperation(profileID, operationName, input)
	if err != nil {
		return APIResponse{}, err
	}
	profile, err := r.ResolveProfile(profileID)
	if err != nil {
		return APIResponse{}, err
	}
	confirmationProfile := &profile
	if r.profiles == nil {
		confirmationProfile = nil
	}
	if !isReadOnlyMethod(operation.Method) {
		if err := r.confirmer.Confirm(ctx, operationConfirmation(r.config, operation, confirmationProfile, arguments)); err != nil {
			return APIResponse{}, errors.New(actionConfirmationError(err))
		}
	}
	return r.execute(ctx, profileID, operation, input)
}

func (r *ExtensionRuntime) execute(ctx context.Context, profileID string, operation OperationDescriptor, input ToolInput) (APIResponse, error) {
	auth, profile, err := r.authForRequest(ctx, profileID)
	if err != nil {
		return APIResponse{}, err
	}
	requestConfig := r.config
	requestConfig.Auth = auth
	response, err := ExecuteOperation(ctx, operation, input, requestConfig, nil)
	if profile != nil {
		response.Profile = profile
	}
	return response, err
}

func (r *ExtensionRuntime) ExecuteBatch(ctx context.Context, request ExtensionBatchRequest) ([]ExtensionBatchResult, error) {
	if strings.TrimSpace(request.Action) == "" {
		return nil, errors.New("extension batch action is required")
	}
	if len(request.Items) == 0 {
		return nil, errors.New("extension batch requires at least one item")
	}
	if len(request.Items) > maxExtensionBatchItems {
		return nil, fmt.Errorf("extension batch exceeds %d items", maxExtensionBatchItems)
	}

	operations := make([]OperationDescriptor, len(request.Items))
	confirmationItems := make([]map[string]any, len(request.Items))
	seenIDs := map[string]bool{}
	for i, item := range request.Items {
		item.ID = strings.TrimSpace(item.ID)
		if item.ID == "" {
			return nil, fmt.Errorf("extension batch item %d requires an id", i+1)
		}
		if seenIDs[item.ID] {
			return nil, fmt.Errorf("duplicate extension batch item id %q", item.ID)
		}
		seenIDs[item.ID] = true
		operation, err := r.validateEnabledOperation(request.Profile, item.Operation, item.Input)
		if err != nil {
			return nil, fmt.Errorf("batch item %q: %w", item.ID, err)
		}
		if isReadOnlyMethod(operation.Method) {
			return nil, fmt.Errorf("batch item %q: operation %q is read-only and does not belong in a write batch", item.ID, operation.ToolName)
		}
		operations[i] = operation
		confirmationItems[i] = map[string]any{
			"id":          item.ID,
			"operation":   operation.ToolName,
			"method":      strings.ToUpper(operation.Method),
			"destination": strings.TrimRight(operation.ServerURL, "/") + "/" + strings.TrimLeft(operation.Path, "/"),
			"input":       item.Input,
		}
	}

	profile, err := r.ResolveProfile(request.Profile)
	if err != nil {
		return nil, err
	}
	confirmationProfile := &profile
	if r.profiles == nil {
		confirmationProfile = nil
	}
	if err := r.confirmer.Confirm(ctx, ActionConfirmation{
		ApplicationName: r.config.Name,
		Profile:         confirmationProfile,
		Action:          request.Action,
		Details: map[string]any{
			"summary":     request.Details,
			"itemCount":   len(request.Items),
			"stopOnError": request.StopOnError,
			"items":       confirmationItems,
		},
	}); err != nil {
		return nil, errors.New(actionConfirmationError(err))
	}
	for i, item := range request.Items {
		operation, err := r.validateEnabledOperation(request.Profile, item.Operation, item.Input)
		if err != nil {
			return nil, fmt.Errorf("batch item %q was disabled or invalidated after approval: %w", item.ID, err)
		}
		if isReadOnlyMethod(operation.Method) {
			return nil, fmt.Errorf("batch item %q changed to a read-only operation after approval", item.ID)
		}
		operations[i] = operation
	}

	auth, selectedProfile, err := r.authForRequest(ctx, request.Profile)
	if err != nil {
		return nil, err
	}
	requestConfig := r.config
	requestConfig.Auth = auth
	results := make([]ExtensionBatchResult, 0, len(request.Items))
	for i, item := range request.Items {
		response, executeErr := ExecuteOperation(ctx, operations[i], item.Input, requestConfig, nil)
		if selectedProfile != nil {
			response.Profile = selectedProfile
		}
		result := ExtensionBatchResult{ID: item.ID, Operation: operations[i].ToolName}
		if executeErr != nil {
			result.Error = executeErr.Error()
		} else {
			result.Response = &response
			if !response.OK {
				result.Error = fmt.Sprintf("upstream returned %s", response.StatusText)
			}
		}
		results = append(results, result)
		if result.Error != "" && request.StopOnError {
			break
		}
	}
	return results, nil
}

func (r *ExtensionRuntime) validateEnabledOperation(profileID, operationName string, input ToolInput) (OperationDescriptor, error) {
	operation, ok := r.operations[strings.TrimSpace(operationName)]
	if !ok {
		return OperationDescriptor{}, fmt.Errorf("unknown OpenAPI operation tool %q", operationName)
	}
	input.Profile = ""
	bodyPresent := input.Body != nil
	if err := validateToolInput(operation, input, bodyPresent); err != nil {
		return OperationDescriptor{}, fmt.Errorf("invalid input for %q: %w", operation.ToolName, err)
	}
	if r.profiles != nil {
		runtime, err := r.profiles.resolve(profileID)
		if err != nil {
			return OperationDescriptor{}, err
		}
		if !ShouldExposeOperation(operation, runtime.policy.filter()) {
			return OperationDescriptor{}, fmt.Errorf("operation %q is disabled for profile %q", operation.ToolName, runtime.profile.ID)
		}
	} else {
		filter := r.filter
		if r.policy != nil {
			filter = r.policy.filter()
		}
		if !ShouldExposeOperation(operation, filter) {
			return OperationDescriptor{}, fmt.Errorf("operation %q is disabled by the current tool policy", operation.ToolName)
		}
	}
	return operation, nil
}

func (r *ExtensionRuntime) authForRequest(ctx context.Context, profileID string) (ResolvedAuthConfig, *APIProfile, error) {
	if r.profiles != nil {
		runtime, err := r.profiles.resolve(profileID)
		if err != nil {
			return ResolvedAuthConfig{}, nil, err
		}
		auth, err := runtime.credentials.authForRequest(ctx)
		if err != nil {
			return ResolvedAuthConfig{}, nil, err
		}
		profile := runtime.profile
		return auth, &profile, nil
	}
	auth, err := r.credentials.authForRequest(ctx)
	return auth, nil, err
}

func validateExtensionTools(extensions []ExtensionTool, operations []OperationDescriptor) error {
	reserved := map[string]bool{}
	for _, operation := range operations {
		reserved[operation.ToolName] = true
	}
	for i, extension := range extensions {
		if extension.Tool == nil || strings.TrimSpace(extension.Tool.Name) == "" {
			return fmt.Errorf("extension tool %d requires a definition and name", i+1)
		}
		if extension.Handler == nil {
			return fmt.Errorf("extension tool %q requires a handler", extension.Tool.Name)
		}
		if isReservedToolName(extension.Tool.Name) || reserved[extension.Tool.Name] {
			return fmt.Errorf("extension tool name %q conflicts with an existing tool", extension.Tool.Name)
		}
		reserved[extension.Tool.Name] = true
	}
	return nil
}

func addExtensionTools(server *mcp.Server, extensions []ExtensionTool, runtime *ExtensionRuntime) {
	for _, extension := range extensions {
		extension := extension
		server.AddTool(extension.Tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return extension.Handler(ctx, runtime, req)
		})
	}
}

func operationMap(operations []OperationDescriptor) map[string]OperationDescriptor {
	result := make(map[string]OperationDescriptor, len(operations))
	for _, operation := range operations {
		result[operation.ToolName] = operation
	}
	return result
}

func DecodeExtensionInput(req *mcp.CallToolRequest, destination any) error {
	return decodeStrictToolInput(req, destination)
}

func ExtensionJSONResponse(message string, result map[string]any) *mcp.CallToolResult {
	return jsonToolResponse(message, result)
}

func ExtensionError(message string) *mcp.CallToolResult {
	return toolError(message)
}
