package apimcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type searchAvailableToolsInput struct {
	Query       string `json:"query,omitempty"`
	EnabledOnly *bool  `json:"enabledOnly,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

func addToolPolicyTools(server *mcp.Server, policy *toolPolicyManager, registry *operationToolRegistry) {
	closedWorld := false
	destructive := true
	emptySchema := strictEmptyObjectSchema()
	var configureMu sync.Mutex

	server.AddTool(&mcp.Tool{
		Name:        toolConfigurationToolName,
		Title:       "Get tool configuration",
		Description: "Show the user-local API tool configuration, its file location, and the number of enabled API endpoints. This does not expose credentials.",
		InputSchema: emptySchema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireNoToolArguments(req); err != nil {
			return toolError(err.Error()), nil
		}
		current, path := policy.snapshot()
		enabledCount := policy.enabledEndpointCount()
		return jsonToolResponse("Current user-local tool configuration.", map[string]any{
			"configPath":           path,
			"configuration":        current,
			"enabledEndpointCount": enabledCount,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        toolFeaturesToolName,
		Title:       "List tool features",
		Description: "Explain the named API feature groups, whether each is enabled, its risk level, and which endpoint selectors it controls.",
		InputSchema: emptySchema,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireNoToolArguments(req); err != nil {
			return toolError(err.Error()), nil
		}
		return jsonToolResponse("Available API feature groups.", map[string]any{"features": policy.features()}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        toolSearchToolName,
		Title:       "Search available tools",
		Description: "Search and explain API endpoints by name, purpose, HTTP method, or path. Results show whether each endpoint is currently enabled and which named features include it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Optional text matched against tool name, title, description, HTTP method, and path.",
				},
				"enabledOnly": map[string]any{
					"type":        "boolean",
					"description": "When true, return only enabled endpoints; when false, only disabled endpoints. Omit for both.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     200,
					"description": "Maximum results. Defaults to 50.",
				},
			},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input searchAvailableToolsInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		endpoints := policy.endpoints(input.Query, input.EnabledOnly, input.Limit)
		return jsonToolResponse("API endpoint explanations.", map[string]any{
			"count":     len(endpoints),
			"endpoints": endpoints,
		}), nil
	})

	server.AddTool(&mcp.Tool{
		Name:        configureToolsToolName,
		Title:       "Configure tools",
		Description: "Update the persistent user-local API tool configuration by enabling or disabling named features and individual endpoint selectors. Before calling, explain the exact access change and obtain explicit user confirmation. Enabling write endpoints expands the actions available to the AI. Changes take effect immediately. Use list_tool_features and search_available_tools first when the request is ambiguous.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"addFeatures":     stringArraySchema("Named feature IDs to enable."),
				"removeFeatures":  stringArraySchema("Named feature IDs to disable."),
				"addEndpoints":    stringArraySchema("Endpoint selectors to enable, such as a tool name or `POST /resource`."),
				"removeEndpoints": stringArraySchema("Endpoint selectors to disable. Removing an endpoint also blocks it when an enabled feature would otherwise include it."),
			},
			"additionalProperties": false,
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		configureMu.Lock()
		defer configureMu.Unlock()
		var input ConfigureToolsInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}
		current, filter, err := policy.configure(input)
		if err != nil {
			return toolError(err.Error()), nil
		}
		registry.apply(filter)
		_, path := policy.snapshot()
		enabledCount := policy.enabledEndpointCount()
		return jsonToolResponse("Updated the user-local tool configuration. The MCP tool list has been refreshed.", map[string]any{
			"configPath":           path,
			"configuration":        current,
			"enabledEndpointCount": enabledCount,
		}), nil
	})
}

func strictEmptyObjectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
	}
}

func stringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "string"},
		"uniqueItems": true,
	}
}

func requireNoToolArguments(req *mcp.CallToolRequest) error {
	if req == nil || req.Params == nil || len(bytes.TrimSpace(req.Params.Arguments)) == 0 || bytes.Equal(bytes.TrimSpace(req.Params.Arguments), []byte("{}")) {
		return nil
	}
	return errors.New("this tool accepts no arguments")
}

func decodeStrictToolInput(req *mcp.CallToolRequest, destination any) error {
	if req == nil || req.Params == nil || len(bytes.TrimSpace(req.Params.Arguments)) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
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

func jsonToolResponse(message string, result map[string]any) *mcp.CallToolResult {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s\n%s", message, data)}},
		StructuredContent: result,
	}
}
