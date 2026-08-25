# Security Policy

## Supported versions

Security fixes are made on the latest release line. Users should upgrade to the newest published version before reporting an issue that may already be fixed.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through the repository's GitHub Security Advisory form. Do not open a public issue for credential exposure, authentication bypasses, policy bypasses, request smuggling, unsafe file access, or similar security problems.

Include the affected version, platform, configuration shape with all secrets removed, reproduction steps, and expected impact. Never include an API token, password, Keychain export, private API response, or customer accounting data.

You should receive an acknowledgement within seven days. We will coordinate validation, remediation, and disclosure with the reporter.

## Security boundaries

- OpenAPI documents and server configuration are trusted local inputs.
- API responses are untrusted and may contain sensitive data even when credentials are redacted.
- Keychain integration protects credential storage and model visibility; it does not make a compromised local user account or executable safe.
- A tool allowlist limits which requests api-mcp can generate. It is not a substitute for least-privilege credentials or upstream API authorization.
