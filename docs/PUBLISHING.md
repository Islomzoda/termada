# Publishing & distribution

Termada is two things to a marketplace: an **MCP server** (the binary) and a
**Claude Code plugin** (this repo bundles the MCP config + the usage skill). This
doc records what is already live and the exact steps that still need *your*
account (logins / OAuth that an assistant cannot complete for you).

## ✅ Already live — Claude Code plugin marketplace

This repository is a Claude Code plugin marketplace. The moment the
`.claude-plugin/marketplace.json` + `.claude-plugin/plugin.json` (and
`skills/termada/`) are on the default branch, anyone can install it:

```text
/plugin marketplace add Islomzoda/termada
/plugin install termada@termada
```

That installs the usage skill and registers the `termada` MCP server. The user
still needs the `termada` binary on PATH — via `./install.sh`, a GitHub release,
or Homebrew (below). No central approval/review gate; nothing more to submit.

## ▢ Needs your account — MCP registries

All package registries require *your* credentials to publish, so these are
hand-offs. None can be done without you logging in.

### Prerequisite: a published package artifact

The official registry points at a package hosted on npm / PyPI / OCI (Docker).
Termada currently ships only GitHub Releases + a Homebrew tap, so publish a
container image first. `server.json` already references `ghcr.io/islomzoda/termada`.

Add to `.goreleaser.yaml` (then it builds on the next `git tag` release):

```yaml
dockers:
  - image_templates: ["ghcr.io/islomzoda/termada:{{ .Version }}", "ghcr.io/islomzoda/termada:latest"]
    dockerfile: Dockerfile
    build_flag_templates: ["--platform=linux/amd64"]
```

and ensure `.github/workflows/release.yml` logs in before goreleaser runs:

```yaml
- uses: docker/login-action@v3
  with: { registry: ghcr.io, username: ${{ github.actor }}, password: ${{ secrets.GITHUB_TOKEN }} }
```

(GHCR needs `packages: write` permission on the release job.) This is left for you
to apply + verify in CI, since a broken docker step would fail the release.

### Official MCP Registry (registry.modelcontextprotocol.io)

```bash
# 1. install the publisher CLI — download the prebuilt `mcp-publisher` binary
#    from https://github.com/modelcontextprotocol/registry/releases
#    (the `go install .../cmd/mcp-publisher` path does NOT exist in the module).
#    e.g. on macOS:
#    curl -fsSL https://github.com/modelcontextprotocol/registry/releases/latest/download/mcp-publisher_darwin_arm64.tar.gz | tar xz

# 2. (recommended) regenerate/validate server.json against the CURRENT schema —
#    this is the source of truth for the `packages` block (ours is a draft):
./mcp-publisher init      # writes a fresh server.json; merge our name/description/oci package

# 3. authenticate — THIS step is yours (opens a GitHub OAuth device flow in your browser)
./mcp-publisher login github

# 4. publish (needs the ghcr.io/islomzoda/termada image to exist — i.e. after a release)
./mcp-publisher publish
```

The `io.github.Islomzoda/...` namespace is proven by the GitHub login. The
`<!-- mcp-name: io.github.Islomzoda/termada -->` marker is already in the README.

### Third-party directories

- **mcp.so**, **Glama** (`glama.ai/mcp`) — largely crawl public GitHub repos that
  carry the `mcp-name` marker. The marker is in place; they should pick it up, or
  submit the repo URL on their site.
- **Smithery** (`smithery.ai`) — sign in with GitHub, "Add server", point at this
  repo. (Your account.)
- **awesome-mcp-servers** — open a PR adding Termada to
  `https://github.com/punkpeye/awesome-mcp-servers` (fork + PR; your GitHub).

## Homebrew (currently formula-only, not pushed)

`brews:` in `.goreleaser.yaml` has `skip_upload: "true"` — goreleaser builds the
formula but does NOT push it, because the `Islomzoda/homebrew-tap` repo doesn't
exist yet and the default `GITHUB_TOKEN` can't write to a separate repo anyway.
(Pushing to a non-existent tap is what turned the v0.7.0 release red.)

To enable `brew install islomzoda/termada/termada`:
1. Create a public repo `Islomzoda/homebrew-tap`.
2. Create a PAT (classic, `repo` scope) and add it as the `HOMEBREW_TAP_TOKEN`
   secret on the termada repo.
3. In `.goreleaser.yaml`: remove `skip_upload`, and set
   `brews[].repository.token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"`.
4. In `release.yml`, pass `HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}`
   in the goreleaser step's `env:`.
