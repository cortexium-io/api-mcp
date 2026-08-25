# Security Policy

## Supported versions

Security fixes are made on the latest release line. Users should upgrade to the newest published version before reporting an issue that may already be fixed.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through the repository's GitHub Security Advisory form. Do not open a public issue for credential exposure, authentication bypasses, policy bypasses, request smuggling, unsafe file access, or similar security problems.

Include the affected version, platform, configuration shape with all secrets removed, reproduction steps, and expected impact. Never include an API token, password, Keychain export, private API response, or customer accounting data.

You should receive an acknowledgement within seven days. We will coordinate validation, remediation, and disclosure with the reporter.

## Security boundaries

- Local OpenAPI documents and server configuration are trusted inputs. Remote specs and external references require explicit opt-in; authenticated configurations using them must pin `serverUrl`.
- API responses are untrusted and may contain sensitive data even when credentials are redacted.
- Keychain integration protects credential storage and model visibility; it does not make a compromised local user account or executable safe.
- Generated tools reject input not declared by the OpenAPI operation, and a tool allowlist limits which operations api-mcp can generate. Neither is a substitute for least-privilege credentials or upstream API authorization.
- On macOS, non-read API calls and policy/profile changes require approval in a native local dialog after validation and before credentials or side effects. This is server-enforced and does not trust a confirmation flag supplied by an MCP client or model. Other platforms fail closed for these actions.
