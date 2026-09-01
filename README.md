# api-mcp

> [!WARNING]
> **Beta / AI-generated code:** This is experimental beta software, and much of its code and documentation was generated with AI assistance under human direction and review. Automated tests, code signing, and notarization do not guarantee correctness or security. Review the source and test with non-production credentials and data before relying on it, especially before enabling write operations.

`api-mcp` turns an OpenAPI 3.x document into a Model Context Protocol server. Each exposed OpenAPI operation becomes one MCP tool that forwards calls to the underlying HTTP endpoint.

It runs locally over stdio, supports environment- and macOS Keychain-backed credentials, and can expose a fixed or user-managed subset of an API.

## Highlights

- Generate MCP tools from local or remote OpenAPI 3.x documents.
- Keep API credentials outside model-visible configuration and tool arguments.
- Start managed configurations read-only and let users explicitly enable named features or individual endpoints.
- Isolate credentials and tool policies across multiple local account profiles.
- Require a local native macOS approval for policy/profile changes and every non-read API request.
- Redact exact configured credential values from upstream responses and errors.

`api-mcp` is an independent open-source project. It is not affiliated with OpenAI, Anthropic, or any API provider used with it.

## Install

Download a platform archive from GitHub Releases, or build from source with Go 1.27 or newer:

```bash
go install github.com/cortexium-io/api-mcp/cmd/api-mcp@latest
```

Release archives contain the executable, README, and MIT license. Official macOS archives are signed with a Developer ID certificate and notarized by Apple. macOS Keychain integration requires a macOS build with cgo enabled; other builds continue to support environment-backed credentials.

## Build

Build the generic server binary. It reads config from `--config`, `API_MCP_CONFIG`, or `api-mcp.config.json` in the current directory.

```bash
go build -o bin/api-mcp ./cmd/api-mcp
```

## Build With Embedded Config

Create a binary that does not need a separate config file by passing embedded config at Go build time:

```bash
go build \
  -o bin/example-api-mcp \
  -ldflags "$(go run ./cmd/api-mcp-embed-config --config api-mcp.config.json)" \
  ./cmd/api-mcp
```

`api-mcp` itself does not have a build command. `cmd/api-mcp-embed-config` is only a build-time helper that prints the linker flag used by `go build`.

If the config uses `valueEnv`, `tokenEnv`, `usernameEnv`, `passwordEnv`, or `keychain`, only the credential reference is embedded. The secret value is still read at runtime:

```bash
EXAMPLE_API_TOKEN=... bin/example-api-mcp
```

If the config uses inline values, those values are embedded too:

```json
{
  "spec": "./testdata/openapi.yaml",
  "auth": {
    "type": "apiKey",
    "in": "headers",
    "name": "X-Access-Token",
    "value": "your-token-here"
  }
}
```

Then the generated binary can run without a token environment variable:

```bash
bin/example-api-mcp
```

Treat a binary with inline credentials as a secret. Do not commit it or share it with people who should not have the embedded token.
Prefer environment or Keychain-backed credentials instead of embedding secrets.

For local OpenAPI specs, the build-time helper stores an absolute spec path in the embedded config. The spec file must still exist at that path unless `spec` is an HTTP URL.

## Configure

Create `api-mcp.config.json` or pass `--config path/to/config.json`.

```json
{
  "name": "my-api-mcp",
  "spec": "./openapi.yaml",
  "serverUrl": "https://api.example.com",
  "auth": {
    "type": "bearer",
    "tokenEnv": "API_TOKEN"
  },
  "headers": {
    "User-Agent": "api-mcp/0.1.0"
  },
  "tools": {
    "include": ["GET /v1/users/*", "create_user"],
    "exclude": ["*delete*"]
  }
}
```

`spec` is a local file path by default. Remote documents require an explicit trust decision:

```json
{
  "spec": "https://docs.example.com/openapi.json",
  "allowRemoteSpec": true,
  "serverUrl": "https://api.example.com"
}
```

Remote specs must use HTTPS. External `$ref` loading is disabled by default and requires `"allowExternalRefs": true`. When authentication is enabled with either a remote spec or external references, `serverUrl` is required so remotely controlled OpenAPI content cannot redirect credentials to another destination. For local single-file specs, `serverUrl` remains optional and the first matching OpenAPI `servers` entry is used.

## Auth

Supported auth config:

```json
{ "type": "none" }
{ "type": "bearer", "tokenEnv": "API_TOKEN" }
{ "type": "basic", "usernameEnv": "API_USER", "passwordEnv": "API_PASSWORD" }
{ "type": "apiKey", "in": "headers", "name": "X-API-Key", "valueEnv": "API_KEY" }
{ "type": "apiKey", "in": "query", "name": "api_key", "valueEnv": "API_KEY" }
{ "type": "apiKey", "in": "cookies", "name": "api_key", "valueEnv": "API_KEY" }
{ "type": "apiKey", "in": "headers", "name": "X-API-Key", "keychain": { "service": "api-mcp.example", "account": "default" } }
{ "type": "headers", "headers": { "X-Custom-Auth": "${API_TOKEN}" } }
```

Inline credential values are also supported with `token`, `username`, `password`, `value`, and literal `headers` values. Those inline values are included when you build a binary with embedded config, but environment variables are safer for local use.

For bearer tokens and API keys, `keychain` supplies the credential. For basic auth, it supplies the password while the username still comes from `username` or `usernameEnv`. If both an environment-variable name and `keychain` are configured, a non-empty environment value wins and Keychain is the fallback. Inline values retain their existing highest precedence.

## macOS Keychain

Add a Keychain fallback without storing the credential in JSON or in the binary:

```json
{
  "auth": {
    "type": "apiKey",
    "in": "headers",
    "name": "X-API-Key",
    "valueEnv": "EXAMPLE_API_KEY",
    "keychain": {
      "service": "api-mcp.example",
      "account": "default",
      "promptOnMissing": true
    }
  }
}
```

Store the credential using a user-run interactive command:

```bash
bin/api-mcp keychain store --config examples/managed.config.example.json
bin/api-mcp keychain status --config examples/managed.config.example.json
```

The store command requires a terminal, reads the credential twice without echo, and does not accept it through command-line arguments or piped input. It calls macOS Keychain Services directly and stores a non-synchronizing, device-only item that is available while the device is unlocked. There is intentionally no command or MCP tool that prints the stored value.

With `promptOnMissing: true`, a missing Keychain item no longer prevents the MCP
from starting. The first generated API tool call opens a native macOS
secure-input dialog, stores the confirmed value directly in Keychain, and then
continues the request. The server also exposes two zero-argument management
tools: `api_mcp_setup_credential` opens the dialog explicitly, while
`api_mcp_credential_status` reports only whether a Keychain item exists. Leave
the option unset to retain fail-fast startup behavior.

Before an API response or request error is returned to the model, exact configured credential values are replaced with `[REDACTED]`. This protects against an upstream service echoing the credential in a body, header, status, or error message.

Keychain support requires macOS with cgo enabled. Other platforms and `CGO_ENABLED=0` builds return an explicit unsupported error; environment-backed auth continues to work unchanged.

## Filtering

`tools.include` and `tools.exclude` accept exact strings or `*` wildcards. A pattern can match:

- generated tool name, such as `get_pet_by_id`
- OpenAPI `operationId`
- HTTP operation, such as `GET /pet/{petId}`
- tag selector, such as `tag:pets`

Exclude rules win over include rules.

Set `tools.defaultPolicy` to `"deny"` when an empty include list must expose no operations. Omitting it preserves the original behavior where an empty include list allows all operations.

## Persistent Tool Policy

For an MCP that starts safely and lets the user manage its tool surface through ChatGPT, Codex, or Claude, configure `toolPolicy` instead of the fixed `tools` filter:

```json
{
  "name": "my-api-mcp",
  "spec": "./openapi.yaml",
  "toolPolicy": {
    "userConfigDirectory": "My API Plugin",
    "userConfigFile": "config.json",
    "catalog": "./features.json",
    "defaultFeatures": ["read-only"]
  }
}
```

The catalog is bundled with the server configuration and gives stable, human-readable names to endpoint groups:

```json
{
  "version": 1,
  "features": [
    {
      "id": "read-only",
      "title": "Read-only API access",
      "description": "Read records without changing them.",
      "risk": "read",
      "endpoints": ["GET *"]
    },
    {
      "id": "record-writes",
      "title": "Create and update records",
      "description": "Create records and update existing records.",
      "risk": "write",
      "endpoints": ["POST /records", "PATCH /records/*"]
    }
  ]
}
```

On first server start, api-mcp creates `<os-user-config-directory>/<userConfigDirectory>/<userConfigFile>` with owner-only permissions. The initial file contains only `defaultFeatures`:

```json
{
  "version": 1,
  "enabledFeatures": ["read-only"],
  "enabledEndpoints": [],
  "disabledEndpoints": []
}
```

The managed policy is always deny-by-default. Enabled features and endpoints add operations; `disabledEndpoints` wins over both. Feature and endpoint selectors are validated against the loaded OpenAPI document before the file is changed. api-mcp refuses a configuration containing both `tools` and `toolPolicy` so there is only one source of truth.

With `toolPolicy` enabled, the MCP adds these management tools:

- `get_tool_configuration`: read the active policy and its local path.
- `list_tool_features`: explain named features, risks, endpoint selectors, and enabled state.
- `search_available_tools`: search all OpenAPI operations and report their purpose and enabled state.
- `configure_tools`: add or remove named features and endpoint selectors, save atomically, and refresh the live MCP tool list.

`configure_tools` is the only tool that mutates the policy. Its input accepts `addFeatures`, `removeFeatures`, `addEndpoints`, and `removeEndpoints`. Before saving, api-mcp opens a native macOS approval dialog that shows the exact requested change and resulting policy. Approval is handled locally by the server rather than by a model-supplied flag or token. Tool availability is separate from authorization to perform an API write.

Manual edits are loaded on server start. Changes made through `configure_tools` take effect immediately.

## Multiple Account Profiles

Add `profiles` when one local MCP should serve multiple API accounts with separate credentials and tool policies:

```json
{
  "auth": {
    "type": "apiKey",
    "in": "headers",
    "name": "X-API-Key",
    "keychain": {
      "service": "api-mcp.example",
      "account": "default",
      "promptOnMissing": true
    }
  },
  "toolPolicy": {
    "userConfigDirectory": "My API Plugin",
    "userConfigFile": "config.json",
    "catalog": "./features.json",
    "defaultFeatures": ["read-only"]
  },
  "profiles": {
    "registryFile": "accounts.json",
    "defaultProfileId": "default",
    "defaultProfileName": "Default API account"
  }
}
```

Profiles require a managed `toolPolicy` and Keychain authentication with `promptOnMissing: true`. On first start, api-mcp creates a registry containing the default profile. That profile deliberately reuses the configured Keychain account and tool-policy file, preserving an existing single-account installation in place.

New profiles use a derived Keychain account named `profile:<profile-id>` and a policy file under `<user-config-directory>/profiles/<profile-id>/tools.json`. The registry contains IDs and friendly names only; it never contains credential values.

Profile-aware servers add these tools:

- `list_api_profiles`: list profiles, credential availability, policy paths, and enabled endpoint counts.
- `add_api_profile`: create a profile and open the native secure credential prompt. New IDs use the default policy. A preserved policy is never restored implicitly; the caller must pass `restoreToolConfiguration: true`, and the local approval dialog shows that choice.
- `rename_api_profile`: change the friendly name without changing credentials or policy.
- `remove_api_profile`: remove a profile, with separate opt-in deletion of its Keychain item and policy file.
- `api_mcp_credential_status` and `api_mcp_setup_credential`: inspect or set up the selected profile's credential.
- The policy-management tools accept a `profile` selector and operate only on that profile.

Generated API tools also accept `profile`. It is optional while exactly one profile exists and required when multiple profiles exist. There is no global active profile, so concurrent conversations cannot silently redirect later calls by switching shared state. API responses identify the profile that was used.

The MCP tool list is the union of operations enabled by all configured profiles. Before resolving a credential or sending HTTP, the server checks the selected profile's own policy and rejects tools disabled there. Therefore a write tool enabled for one account may be visible to all clients, but it cannot be executed against another account unless that account's policy also enables it.

Adding, renaming, or removing a profile also requires a separate native macOS approval showing the selected account and exact arguments. Profile and policy mutations update the current server process immediately. Other already-running api-mcp processes should be restarted to reload changes made by another client or by manual file edits.

## Runtime Diagnostics

Every server exposes `api_mcp_diagnostics`. It reports the running implementation name and version, OpenAPI file and operation count, response-size limit, credential availability, profile identities, and current feature policies. It never returns credential material.

The tool describes the process that received the call. A client attaching an older process, or blocking a call before dispatch, cannot be detected from inside that process; compare the returned implementation/spec details with the installed plugin and restart the client when they differ.

## Tool Input

Generated tools use a stable input shape:

```json
{
  "path": { "petId": 123 },
  "query": { "includeDetails": true },
  "headers": { "X-Request-ID": "demo" },
  "cookies": { "session": "abc" },
  "contentType": "application/json",
  "body": { "name": "Fluffy" }
}
```

OpenAPI parameters are grouped by location. Generated tools reject undeclared top-level fields, undeclared parameters, missing required values, unsupported content types, and parameter/body values that fail their OpenAPI schemas before resolving credentials or sending HTTP. Auth credentials are applied from config and are not exposed as tool inputs.

## Response Completeness

`maxResponseChars` is enforced as a byte limit. When an upstream body exceeds it, api-mcp discards the partial body, returns `ok: false`, and includes structured `truncation` metadata with code `response_body_truncated` and retry advice. Partial JSON is never returned as a valid collection.

When a JSON response contains supported paging metadata such as `meta.paging`, api-mcp also returns normalized `pagination` metadata. `finalPage` is true only when both the current page and page count prove it. Missing paging metadata is not evidence that a collection is complete.

## Custom Workflow Host

The public `github.com/cortexium-io/api-mcp/apimcp` package lets a thin API-specific binary register custom MCP tools without owning credentials, profile state, policy enforcement, or HTTP transport. Extensions can execute enabled read operations or submit one exact write batch. A batch is fully validated first, receives one native approval showing every item, and only then resolves credentials and performs those exact calls.

There is intentionally no public unconfirmed-write method. API-specific review and preview/commit logic belongs in the API plugin, while api-mcp retains the shared security boundary.

## Run

```bash
go build -o bin/api-mcp ./cmd/api-mcp
bin/api-mcp --config examples/config.example.json
```

For MCP clients, point the command at `/absolute/path/to/bin/api-mcp --config /absolute/path/to/api-mcp.config.json`.

## Security model

Local OpenAPI documents and configuration are trusted inputs. Remote OpenAPI loading and external references are explicit opt-ins, and authenticated remote configurations must pin `serverUrl`. Generated tools enforce input schemas and the configured allowlist before resolving credentials or making an HTTP request. Managed policy files are created with owner-only permissions and replaced atomically.

On macOS, every non-read HTTP operation and every policy/profile mutation opens a local native approval dialog after validation and before credentials or side effects. Reads and credential status remain non-interactive. Credential setup uses its own hidden-input dialog. Non-macOS builds fail closed for actions that require native approval; environment-backed read-only API access remains available.

The model never needs to receive a Keychain-backed secret: credential setup uses the local terminal or native macOS secure-input dialog, and management tools report only presence or absence. Exact credentials and environment-substituted secret atoms in custom auth headers are redacted from upstream response bodies and errors as defense in depth; API responses should still be treated as potentially sensitive.

For vulnerabilities, see [SECURITY.md](SECURITY.md).

## Development

```bash
GOTOOLCHAIN=go1.27.0 go test ./...
GOTOOLCHAIN=go1.27.0 go vet ./...
```

Maintainer release instructions are in [docs/RELEASING.md](docs/RELEASING.md).

## License

MIT. See [LICENSE](LICENSE).
