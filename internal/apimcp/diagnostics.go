package apimcp

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type diagnosticsInput struct {
	Profile string `json:"profile,omitempty"`
}

type diagnosticsProfile struct {
	APIProfileStatus
	Configuration UserToolPolicy      `json:"configuration"`
	Features      []ToolFeatureStatus `json:"features"`
}

func addDiagnosticsTool(server *mcp.Server, cfg Config, operations []OperationDescriptor, profiles *profileManager, credentials *credentialManager, policy *toolPolicyManager) {
	closedWorld := false
	server.AddTool(&mcp.Tool{
		Name:        diagnosticsToolName,
		Title:       "Diagnose API MCP configuration",
		Description: "Show the running MCP implementation, OpenAPI contract, response limit, profile and feature-policy state, and credential availability without returning credential material. This cannot prove whether the host attached a different MCP process or blocked a tool call before it reached this server.",
		InputSchema: profileSelectorSchema("Optional stable profile ID. Omit to inspect every configured profile."),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &closedWorld},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input diagnosticsInput
		if err := decodeStrictToolInput(req, &input); err != nil {
			return toolError("invalid tool arguments: " + err.Error()), nil
		}

		result := map[string]any{
			"implementation": map[string]any{
				"name":    cfg.Name,
				"version": cfg.Version,
			},
			"openapi": map[string]any{
				"spec":           cfg.Spec,
				"specFile":       filepath.Base(cfg.Spec),
				"operationCount": len(operations),
			},
			"responseLimits": map[string]any{
				"maxResponseBytes": cfg.MaxResponseChars,
				"behavior":         "oversized response bodies are discarded and returned as response_body_truncated with retry advice",
			},
			"limitations": []string{
				"These diagnostics describe the process that received this call; they cannot detect a different stale process that the host attached instead.",
				"A host-level approval or dispatch failure can occur before this server receives a tool call and is therefore not observable here.",
				"Credential status proves availability only; credential material is never returned.",
			},
		}

		if profiles != nil {
			diagnostics, err := diagnosticsForProfiles(profiles, input.Profile)
			if err != nil {
				return toolError(err.Error()), nil
			}
			result["profiles"] = diagnostics
			result["selectedProfile"] = strings.TrimSpace(input.Profile)
		} else {
			if strings.TrimSpace(input.Profile) != "" {
				return toolError("profile is not supported by this single-account MCP configuration"), nil
			}
			available, stored, err := credentials.statusDetails()
			if err != nil {
				return toolError(err.Error()), nil
			}
			result["credential"] = map[string]any{
				"available":        available,
				"storedInKeychain": stored,
			}
			if policy != nil {
				configuration, path := policy.snapshot()
				result["toolPolicy"] = map[string]any{
					"configPath":           path,
					"configuration":        configuration,
					"enabledEndpointCount": policy.enabledEndpointCount(),
					"features":             policy.features(),
				}
			}
		}

		return jsonToolResponse("API MCP runtime diagnostics. No credential material was read into the conversation.", result), nil
	})
}

func diagnosticsForProfiles(profiles *profileManager, selected string) ([]diagnosticsProfile, error) {
	selected = strings.TrimSpace(selected)
	profiles.mu.Lock()
	runtimes := make([]*apiProfileRuntime, 0, len(profiles.runtimes))
	if selected != "" {
		runtime, ok := profiles.runtimes[selected]
		if !ok {
			profiles.mu.Unlock()
			return nil, fmt.Errorf("unknown profile %q; call list_api_profiles", selected)
		}
		runtimes = append(runtimes, runtime)
	} else {
		for _, runtime := range profiles.runtimes {
			runtimes = append(runtimes, runtime)
		}
	}
	profiles.mu.Unlock()

	result := make([]diagnosticsProfile, 0, len(runtimes))
	for _, runtime := range runtimes {
		status, err := runtime.status()
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", runtime.profile.ID, err)
		}
		configuration, _ := runtime.policy.snapshot()
		result = append(result, diagnosticsProfile{
			APIProfileStatus: status,
			Configuration:    configuration,
			Features:         runtime.policy.features(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}
