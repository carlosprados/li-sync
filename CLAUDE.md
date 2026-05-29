# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`li-sync` is a single-binary Go CLI that audits which posts of a Hugo blog have
been queued/published on LinkedIn, and optionally publishes them via the
LinkedIn API. It is a **sidecar** that operates on an *external* Hugo repo — it
is not the blog repo itself. Read `README.md` for the full user-facing command
reference; this file covers what isn't obvious from the source.

## Commands

```
go build -o li-sync .        # build (binary is gitignored at repo root)
go vet ./...                 # static checks
gofmt -l .                   # list unformatted files (-w to fix)
go mod tidy                  # tidy deps (also run by goreleaser pre-build hook)
```

There are **no tests** in the repo (`*_test.go` absent). If you add functionality
worth covering, propose tests for `parseFrontMatter`, `parseFlexibleTime`,
`classify`, and `resolveAuthCredentials` — the pure logic with branching.

Releases are tag-driven: pushing a `v*` tag triggers `.github/workflows/release.yml`,
which runs GoReleaser (cross-compiles linux/darwin/windows × amd64/arm64). No
manual release steps.

## Architecture

Flat `package main`, split by concern. The CLI is built with **Cobra**
(commands/flags + auto-generated `--help`) and **Viper** (config file + env
binding):

- **`root.go`** — `main()`, the Cobra root command, every subcommand
  constructor (`newStatusCmd`, `newPublishCmd`, `newEditCmd`, `newRepublishCmd`,
  …), and Viper wiring (`initConfig`): persistent `--repo`/`--base-url` flags,
  env vars (`LISYNC_*`, `LINKEDIN_*`), and an optional `config.yaml` in the
  config dir. `repoRoot()` resolves the Hugo root via Viper.
- **`main.go`** — domain logic only: **repo discovery**, post scanning, state
  file I/O, and the `run*` helpers behind `status`/`mark`/`unmark`/`open`. A
  post's `URLSlug` (front-matter `slug:` or dir name) is what builds the
  article URL.
- **`auth.go`** — the one-time OAuth flow (`runAuthFlow`): credential
  resolution, a local callback HTTP server on `:8765`, CSRF `state` check.
- **`linkedin.go`** — LinkedIn HTTP client: token exchange/refresh, userinfo,
  the Posts API calls (create / `PARTIAL_UPDATE` commentary / delete), and the
  publish **preflight** (`verifyArticleOG`). All API constants live here.
- **`publish.go`** — `runPublish` (+ preflight gate), `runEdit`,
  `runRepublish`, `buildPostPayload`, and `siteBaseURL` (Viper).
- **`config.go`** — persistence of app credentials + OAuth tokens to the config dir.

### Two state stores — keep them distinct

This is the central design point. The tool reads/writes **two unrelated
locations**:

1. **`linkedin-status.yaml`** — the source of truth for what's been
   scheduled/published. It lives at the *Hugo site root* (next to `content/`),
   **versioned in the blog's git repo**, not this one. `mark`/`unmark` are
   trust-based edits; `publish` updates it automatically on API success.
2. **`$XDG_CONFIG_HOME/li-sync/`** (override `LI_SYNC_CONFIG_DIR`) — OAuth
   secrets, never versioned: `app.json` (client id/secret, 0600) and
   `tokens.json` (access/refresh tokens + person URN, 0600).

### Repo discovery (precedence)

`resolveRepoRoot` → `--repo` flag > `LISYNC_REPO` env > walk up from cwd until a
dir containing `content/posts/` is found. **`auth` is special-cased to run
before repo resolution** (see `main.go` switch) because authenticating doesn't
need a Hugo repo.

### Post model

A "post" is `content/posts/<slug>/index.md` (YAML `---` or TOML `+++` front
matter; JSON unsupported) plus an optional sibling `linkedin-post.txt`
companion. The companion's full text becomes the LinkedIn `commentary`. The
`status` command's `classify` function maps each post to one of:
`future`/`draft`/`no companion`/`MISSING`/`scheduled`/`published`.

### Publish payload & previews

`publish` sends to the LinkedIn Posts API (`linkedin.go`). If the post's `date:`
(or `--at`) is in the future it sets `publishedAt` (epoch millis) to **schedule**;
if past it publishes immediately with a warning. The article card image is *not*
uploaded — LinkedIn renders it from the `og:*` meta tags of
`<LISYNC_BASE_URL>/posts/<slug>/` (default `https://carlos.enredando.me`,
override via `LISYNC_BASE_URL`). `--dry-run` prints the payload with a
placeholder author URN and needs no auth.

## Gotchas

- **OAuth redirect URI must match exactly.** The "redirect_uri does not match
  the registered value" error (shown in-browser *before* the consent screen)
  means `http://localhost:8765/callback` is not registered (verbatim) under the
  LinkedIn app's *Auth tab → Authorized redirect URLs*. Confirmed real-world
  cause: that list was simply **empty**. The redirect_uri the binary sends was
  verified to match the source — so this is always a portal-config issue, never
  the binary. The port (`callbackPort`) and path (`callbackPath`) are hardcoded
  in `auth.go`; changing them requires updating the LinkedIn app config too.
  Common non-exact mismatches: `https://`, trailing slash, `127.0.0.1`.
- Credential resolution (`resolveAuthCredentials`) has persist semantics: flags
  and interactive prompt are saved to `app.json`; env vars are used but **never
  persisted** (intentionally ephemeral).
- Tokens auto-refresh when within 5 min of expiry (`ensureFreshTokens`); the
  created post URN comes from the `x-restli-id` response header.
- Module path is `github.com/carlosprados/li-sync`; Go 1.25.

## Project conventions

- Code, comments, identifiers in **English**.
- This repo's commit messages: **English**, prefix style (`feat:`, `fix:`,
  `chore:`, `ci:`, `docs:`, `refactor:`, `test:`). Do **not** add
  `Co-Authored-By` trailers. Never commit/push without explicit approval.
