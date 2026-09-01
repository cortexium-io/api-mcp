// Package apimcp hosts OpenAPI-backed MCP servers and narrow API-specific
// workflow extensions while retaining credential, policy, approval, and HTTP
// ownership in the core runtime.
package apimcp

import (
	"context"
	"io"
	"os"

	internal "github.com/cortexium-io/api-mcp/internal/apimcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Config = internal.Config
type APIProfile = internal.APIProfile
type APIResponse = internal.APIResponse
type TruncationInfo = internal.TruncationInfo
type PaginationInfo = internal.PaginationInfo
type ToolInput = internal.ToolInput
type ExtensionTool = internal.ExtensionTool
type ExtensionRuntime = internal.ExtensionRuntime
type ExtensionBatchItem = internal.ExtensionBatchItem
type ExtensionBatchRequest = internal.ExtensionBatchRequest
type ExtensionBatchResult = internal.ExtensionBatchResult

var DefaultVersion = internal.DefaultVersion

// LoadConfigFromArgs loads and normalizes the standard api-mcp configuration.
func LoadConfigFromArgs(args []string) (Config, error) {
	return internal.LoadConfigFromArgs(args)
}

// RunKeychainCommand runs the user-interactive Keychain enrollment command.
func RunKeychainCommand(args []string, stdin *os.File, output io.Writer) error {
	return internal.RunKeychainCommand(args, stdin, output)
}

// CreateServer creates an MCP server and registers validated API-specific tools.
func CreateServer(ctx context.Context, cfg Config, extensions ...ExtensionTool) (*mcp.Server, error) {
	return internal.CreateServerWithExtensions(ctx, cfg, extensions)
}

// RunStdio creates the server and serves it over the MCP stdio transport.
func RunStdio(ctx context.Context, cfg Config, extensions ...ExtensionTool) error {
	return internal.RunStdioWithExtensions(ctx, cfg, extensions)
}

// DecodeInput strictly decodes an extension tool's arguments.
func DecodeInput(req *mcp.CallToolRequest, destination any) error {
	return internal.DecodeExtensionInput(req, destination)
}

// JSONResponse returns matching text and structured MCP content.
func JSONResponse(message string, result map[string]any) *mcp.CallToolResult {
	return internal.ExtensionJSONResponse(message, result)
}

// ErrorResponse returns a model-visible MCP tool error.
func ErrorResponse(message string) *mcp.CallToolResult {
	return internal.ExtensionError(message)
}
