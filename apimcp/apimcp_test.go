package apimcp_test

import (
	"context"
	"testing"

	"github.com/cortexium-io/api-mcp/apimcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExternalPackageCanDefineNarrowExtensionTool(t *testing.T) {
	tool := apimcp.ExtensionTool{
		Tool: &mcp.Tool{
			Name:        "example_review",
			Description: "Compile-time check for the public extension boundary.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false},
		},
		Handler: func(_ context.Context, runtime *apimcp.ExtensionRuntime, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if runtime == nil {
				t.Fatal("runtime is nil")
			}
			return apimcp.JSONResponse("ok", map[string]any{"ok": true}), nil
		},
	}
	if tool.Tool.Name != "example_review" || tool.Handler == nil {
		t.Fatalf("invalid extension tool: %+v", tool)
	}
}
