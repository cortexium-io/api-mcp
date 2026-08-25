# Releasing api-mcp

Releases are built by GitHub Actions from signed or annotated version tags. The workflow produces archives for macOS, Linux, and Windows and publishes a SHA-256 checksum file. macOS executables are signed with the hardened runtime and a secure timestamp, then submitted to Apple's notarization service before publication.

## Prerequisites

- The repository is published as `cortexium-io/api-mcp`.
- The default branch is protected and CI is passing.
- GitHub Actions has read access to repository contents and write access to release contents through the workflow's scoped `GITHUB_TOKEN`.
- These GitHub Actions repository secrets are configured:
  - `APPLE_DEVELOPER_ID_P12`: base64-encoded Developer ID Application certificate and private key
  - `APPLE_DEVELOPER_ID_PASSWORD`: password used when exporting the `.p12`
  - `APPLE_NOTARY_APPLE_ID`: Apple Account used for notarization
  - `APPLE_NOTARY_TEAM_ID`: Apple Developer Team ID
  - `APPLE_NOTARY_PASSWORD`: app-specific password for the Apple Account

## Release checklist

1. Update user-facing documentation and confirm `go.mod` uses the intended supported Go release.
2. Run:

   ```bash
   GOTOOLCHAIN=go1.27.0 go test ./...
   GOTOOLCHAIN=go1.27.0 go vet ./...
   GOTOOLCHAIN=go1.27.0 go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
   test -z "$(gofmt -l .)"
   ```

3. Review the complete diff and merge it to the default branch.
4. Create and push an annotated tag such as `v0.1.0`:

   ```bash
   git tag -a v0.1.0 -m "api-mcp v0.1.0"
   git push origin v0.1.0
   ```

5. Confirm the release workflow succeeds.
6. Download one archive, verify it against `SHA256SUMS`, and run `api-mcp --version`. On macOS, also verify the signature with `codesign --verify --strict --verbose=2 api-mcp`.

Tags containing a hyphen, such as `v0.2.0-beta.1`, are published as prereleases.

The release workflow injects the tag version, source commit, and source commit timestamp into each executable. Do not publish locally built binaries as official release artifacts.

The notarization ticket for a ZIP archive is published by Apple for Gatekeeper to retrieve online; the ZIP itself cannot carry a stapled ticket. Linux and Windows archives are checksum-protected but are not platform-code-signed.
