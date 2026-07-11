# Publishing and distribution

Termada ships one Go binary through GitHub Releases, GHCR, Homebrew, deb/rpm
packages, the MCP registry metadata in `server.json`, and the Claude Code plugin
manifest in `.claude-plugin/`.

This document describes the repository configuration and the checks required for
a release. It deliberately does not claim that an external registry is healthy;
verify the published artifacts after every release.

## Release pipeline

Pushing a `v*` tag starts `.github/workflows/release.yml`. The workflow:

1. runs the complete Go test suite;
2. authenticates to GHCR;
3. invokes the pinned, snapshot-tested GoReleaser v2.17.0 using `.goreleaser.yaml`;
4. publishes platform archives, SHA-256 checksums, deb/rpm packages, the
   Linux/amd64 GHCR image and the Homebrew formula;
5. signs `checksums.txt` when both release-signing secrets are configured.

Before invoking GoReleaser, the workflow validates that the signing secrets are
configured as a pair. With both secrets present, it runs the configured signing
pipe. With neither present, it passes `--skip=sign`, so an unsigned release
contains `checksums.txt` and no `checksums.txt.sig`. If exactly one secret is
present, the release fails before any artifact is published.

The workflow uses these repository secrets:

- `HOMEBREW_TAP_TOKEN`: a token that can write to
  `Islomzoda/homebrew-tap`;
- `TERMADA_RELEASE_PRIVKEY` and `TERMADA_RELEASE_PUBKEY`: an optional Ed25519 key
  pair for signed checksums. Configure both or neither. Release builds embed the
  public key, so a keyed build refuses unsigned self-updates.

`GITHUB_TOKEN` is provided by Actions and is used for GitHub Releases and GHCR.

## Version checklist

Before tagging, update the version in all release metadata:

- `cmd/termada/main.go` (`version` fallback);
- `server.json` (`version` and OCI identifier tag);
- `.claude-plugin/plugin.json`;
- `CHANGELOG.md`.

`go test ./cmd/termada` enforces that the first three values stay in sync.

Run the local release gate:

```bash
test -z "$(gofmt -l $(git ls-files '*.go'))"
go mod verify
go vet ./...
go test -race -p 1 ./...
sh -n install.sh
```

Then create and push the tag:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

After Actions completes, verify that the release contains `checksums.txt`, its
non-empty signature when signing is enabled (and no signature asset when it is
disabled), every expected platform archive, packages and the GHCR image.
Exercise `install.sh` and Unix `termada update` against the new release, and
confirm that Windows reports the documented manual-install path, before
announcing it.

## MCP registry

`server.json` points at the versioned GHCR image. After the image exists, follow
the current [official publisher quickstart](https://modelcontextprotocol.io/registry/quickstart)
with an authenticated GitHub session:

```bash
mcp-publisher login github
mcp-publisher publish
```

Run `mcp-publisher validate server.json` before publishing, then still check the
current official publisher help because commands and schemas can change.

## Claude Code plugin

The repository is a Claude Code plugin marketplace through
`.claude-plugin/marketplace.json`. Consumers register and install it with:

```text
/plugin marketplace add Islomzoda/termada
/plugin install termada@termada
```

The plugin installs the MCP configuration and usage skill. The `termada` binary
must already be available on `PATH`.
