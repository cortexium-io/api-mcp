# Releasing api-mcp

Releases are built by GitHub Actions from signed or annotated version tags. The workflow produces archives for macOS, Linux, and Windows and publishes a SHA-256 checksum file. macOS executables are signed with the hardened runtime and a secure timestamp, then submitted to Apple's notarization service before publication.

## Prerequisites

- The repository is published as `cortexium-io/api-mcp`.
- The default branch is protected and CI is passing.
- A GitHub tag ruleset restricts creation and deletion of tags matching `v*` to trusted maintainers.
- A GitHub Actions environment named `release` exists and requires a trusted reviewer before deployment.
- GitHub Actions has read access to repository contents and write access to release contents through the workflow's scoped `GITHUB_TOKEN`.
- These GitHub Actions **environment secrets on `release`** are configured:
  - `APPLE_DEVELOPER_ID_P12`: base64-encoded Developer ID Application certificate and private key
  - `APPLE_DEVELOPER_ID_PASSWORD`: password used when exporting the `.p12`
  - `APPLE_NOTARY_APPLE_ID`: Apple Account used for notarization
  - `APPLE_NOTARY_TEAM_ID`: Apple Developer Team ID
  - `APPLE_NOTARY_PASSWORD`: app-specific password for the Apple Account

Do not keep the Apple credentials as repository secrets. A workflow on an untrusted ref can request repository secrets without passing through the protected environment. If the values were initially added as repository secrets, recreate them under **Settings → Environments → release**, verify the release workflow can see them, and then delete the repository-secret copies.

The workflow validates semantic-version tag syntax and refuses a tag whose commit is not contained in the default branch. The protected `release` environment and tag ruleset are still required because checks inside a workflow are not a substitute for GitHub-enforced trust boundaries.

## Release checklist

1. Update user-facing documentation and confirm `go.mod` uses the intended supported Go release.
2. Run:

   ```bash
   GOTOOLCHAIN=go1.27.0 go test ./...
   GOTOOLCHAIN=go1.27.0 go vet ./...
   GOTOOLCHAIN=go1.27.0 go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
   test -z "$(gofmt -l .)"
   ```

3. Review the complete diff and merge it to the protected default branch. Confirm the release commit's CI run passed.
4. Create and push the annotated beta tag:

   ```bash
   git tag -a v0.1.0-beta.2 -m "api-mcp v0.1.0-beta.2"
   git push origin v0.1.0-beta.2
   ```

5. Review and approve the `release` environment deployment, then confirm the release workflow succeeds.
6. Download one archive, verify it against `SHA256SUMS`, and run `api-mcp --version`. On macOS, also verify the signature with `codesign --verify --strict --verbose=2 api-mcp`.

Tags containing a hyphen, such as `v0.1.0-beta.1`, are published as prereleases. Keep the beta and AI-generated-code warning in the README and automated release notes until the project has undergone further independent review and is deliberately promoted to a stable release.

The release workflow injects the tag version, source commit, and source commit timestamp into each executable. Do not publish locally built binaries as official release artifacts.

The notarization ticket for a ZIP archive is published by Apple for Gatekeeper to retrieve online; the ZIP itself cannot carry a stapled ticket. Linux and Windows archives are checksum-protected but are not platform-code-signed.
