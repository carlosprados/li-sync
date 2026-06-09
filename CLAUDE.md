# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`li-sync` is a single-binary Go CLI that audits which posts of a Hugo blog have
been queued/published on LinkedIn, and optionally publishes them via the
LinkedIn API. It also posts companion tweets to **X (Twitter)** via the `x`
command tree. It is a **sidecar** that operates on an *external* Hugo repo — it
is not the blog repo itself. Read `README.md` for the full user-facing command
reference; this file covers what isn't obvious from the source.

**Design principle (load-bearing): the CLI is first-class and must stay fully
scriptable and non-interactive** — an automated agent must be able to drive
every operation through flags/stdout/stderr with no TTY. Any TUI (planned, on
Bubble Tea) is strictly **additive** (a separate `tui` command), never a
replacement. This is why the core logic is being decoupled from presentation
(see `buildStatusReport`, `Reporter`, `PublishResult`): so the CLI and a TUI can
share one core without either becoming the only way in.

## Commands

This repo uses **Task** (`go-task`); see `Taskfile.yml`. Prefer it over raw `go`
invocations:

```
task build        # build the li-sync binary (gitignored at repo root)
task vet          # go vet ./...
task fmt          # gofmt -w .   (task fmt:check fails if anything is unformatted)
task tidy         # go mod tidy && go mod verify
task test         # go test ./...
task check        # fmt:check + vet + build + test — the pre-push gate
task run -- <args># build then run, e.g. `task run -- status --all`
```

The equivalent raw commands still work (`go build -o li-sync .`, `go vet ./...`,
`gofmt -l .`, `go mod tidy`).

Tests live alongside the source (`*_test.go`, `package main`) and cover the pure
logic: `parseFrontMatter`, `parseFlexibleTime`, `classify`, `buildStatusReport`,
`applyMentions`, `commentaryUTF16Len`, `resolveAuthCredentials` (non-prompt
branches), state round-trip, `resolveRepoRoot`, and the X counterparts:
`tweetLen`, `classifyX`, the `x-status.yaml` round-trip, the PKCE pair (against
the RFC 7636 Appendix B vector), `buildXAuthorizeURL`, and
`resolveXAuthCredentials`. Run with `task test`. The HTTP/OAuth paths
(`linkedin.go`, `twitter.go`, the browser flows) are not unit-tested — they'd
need an httptest server; add that if you touch them.

Releases are tag-driven: pushing a `v*` tag triggers `.github/workflows/release.yml`,
which runs GoReleaser (cross-compiles linux/darwin/windows × amd64/arm64). No
manual release steps.

## Architecture

Flat `package main`, split by concern. The CLI is built with **Cobra**
(commands/flags + auto-generated `--help`) and **Viper** (config file + env
binding):

- **`root.go`** — `main()`, the Cobra root command, every subcommand
  constructor (`newStatusCmd`, `newPublishCmd`, `newEditCmd`, `newRepublishCmd`,
  `newMarkCmd`, `newUnmarkCmd`, `newOpenCmd`, `newAuthCmd`), and Viper wiring
  (`initConfig`): persistent `--repo`/`--base-url` flags,
  env vars (`LISYNC_*`, `LINKEDIN_*`), and an optional `config.yaml` in the
  config dir. `repoRoot()` resolves the Hugo root via Viper.
- **`main.go`** — domain logic only: **repo discovery**, post scanning, state
  file I/O, and the `run*` helpers behind `status`/`mark`/`unmark`/`open`.
  `buildStatusReport(root, all, now)` returns the status rows + counts as data
  (`StatusReport`) with an injectable `now`; `runStatus` just prints it. A
  post's `URLSlug` (front-matter `slug:` or dir name) is what builds the
  article URL.
- **`auth.go`** — the one-time OAuth flow (`runAuthFlow`): credential
  resolution, a local callback HTTP server on `:8765`, CSRF `state` check.
- **`linkedin.go`** — LinkedIn HTTP client: token exchange/refresh, userinfo,
  the Posts API calls (create / `PARTIAL_UPDATE` commentary / delete), image
  upload (`uploadImage`, for the card thumbnail), and the publish **preflight**
  (`verifyArticleOG`). All API constants live here. NOTE: there is deliberately
  no comment/Social Actions support — that API needs a "Community Management
  API" product grant the app doesn't have (returns 403); the link-in-first-comment
  step is done manually in the UI.
- **`publish.go`** — `runPublish` (+ preflight gate), `runEdit`,
  `runRepublish`, `buildPostPayload`. The publish/republish funcs are
  presentation-free: they take `baseURL`/`mentions` as params (resolved from
  Viper by `root.go`), emit progress via a `Reporter`, and **return a
  `PublishResult`** instead of printing. `root.go`'s commands render it. No
  stdout writes happen in the core publish path.
- **`reporter.go`** — the `Reporter` seam: progress steps from long-running ops.
  The CLI binds `writerReporter` to stderr (verbatim with the old output); the
  TUI binds a `chanReporter` that turns each `Stepf` into a `tea.Msg`.
- **`tui.go`** — the `tui` command's Bubble Tea program (`uiModel`). It is a
  **dual-platform dashboard**: `reload` builds both `buildStatusReport` and
  `buildXStatusReport` (each with `all=true`) and merges them by slug into one
  table with `LINKEDIN` and `X` state columns; a row shows when it's actionable
  on *either* platform (or everything under `[a]`). Read-only browsing plus write
  actions that call the same core funcs behind a confirm. Write actions
  (publish/dry-run/republish/unmark) first open a **platform selector**
  (`modeSelectPlatform`): checkboxes for LinkedIn and X, each pre-selected when
  available — availability is per-action (companion present for publish/dry-run;
  state entry present for republish/unmark). `[e]` edit is LinkedIn-only (X has
  no edit endpoint) and skips the selector. The chosen platforms run
  **sequentially over one shared progress channel**, each step prefixed
  (`prefixReporter`: `[LinkedIn] `/`[X] `); outcomes aggregate independently
  (`doneMsg.failed`), so one platform failing doesn't abort the other. Lives in
  `package main`, not a separate package — the TUI shares the core directly, it
  doesn't need an extracted boundary. State machine:
  `modeBrowse → modeSelectPlatform → modeConfirm → modeRunning → modeResult`
  (dry-run skips `modeConfirm`), plus `modePickRepo` (a `bubbles/filepicker` to
  choose the Hugo root when none is resolved, or via `c`; the pick is validated
  through `resolveRepoRoot`). The `tui` command therefore does NOT fail on a
  missing repo — it opens the picker. `runEdit` returns the URN and `unmarkPost`
  does the state mutation without printing, so the TUI never writes to stdout.
- **`config.go`** — persistence of app credentials + OAuth tokens to the config
  dir, for both platforms (`app.json`/`tokens.json` for LinkedIn,
  `x_app.json`/`x_tokens.json` for X).
- **`twitter.go`** — X HTTP client: token requests (Basic auth for confidential
  clients, body `client_id` for public/PKCE ones), `postTweet`, `deleteTweet`,
  `fetchXUser`, and `ensureFreshXTokens`. All X API constants live here.
- **`xauth.go`** — the X OAuth 2.0 flow (`runXAuthFlow`): Authorization Code +
  PKCE (S256; `generateCodeVerifier`/`codeChallengeS256`), reusing auth.go's
  callback server and CSRF state. `resolveXAuthCredentials` mirrors the
  LinkedIn precedence but the client secret is optional (public clients).
- **`xstate.go`** — the X state store (`x-status.yaml` wrappers), the
  `x-post.txt` companion helpers, `tweetLen` (280 weighted chars, URLs = 23),
  `classifyX`/`buildXStatusReport` (no scheduled state), `runXMark`/`runXUnmark`,
  and `runXOpen` (manual mode: X's web intent with the tweet text pre-filled —
  unlike LinkedIn's composer, the intent accepts the text as a query param, so
  the manual path needs neither copy/paste nor any API setup).
- **`xpublish.go`** — `runXPublish`/`runXRepublish`, presentation-free like
  their LinkedIn counterparts (Reporter + `XPublishResult`). No scheduling: a
  future-dated post is refused. No mention expansion (X @handles are plain text).

### State stores — keep them distinct

This is the central design point. The tool reads/writes **unrelated
locations** that must never merge:

1. **`linkedin-status.yaml`** — the source of truth for what's been
   scheduled/published on LinkedIn. It lives at the *Hugo site root* (next to
   `content/`), **versioned in the blog's git repo**, not this one.
   `mark`/`unmark` are trust-based edits; `publish` updates it automatically on
   API success.
2. **`x-status.yaml`** — same role and schema for X, also at the Hugo site root
   and versioned in the blog repo. Status is only ever `published` (X cannot
   schedule); the note holds the tweet ID. Separate file on purpose — the two
   platform stores share only the YAML plumbing (`loadStateFrom`/`saveStateTo`
   in main.go), never a file.
3. **`$XDG_CONFIG_HOME/li-sync/`** (override `LI_SYNC_CONFIG_DIR`) — OAuth
   secrets, never versioned: `app.json` + `tokens.json` (LinkedIn) and
   `x_app.json` + `x_tokens.json` (X), all 0600.

### Repo discovery (precedence)

`resolveRepoRoot` → `--repo` flag > `LISYNC_REPO` env > walk up from cwd until a
dir containing `content/posts/` is found. **`auth` is special-cased to run
before repo resolution** (see `main.go` switch) because authenticating doesn't
need a Hugo repo.

### Post model

A "post" is `content/posts/<slug>/index.md` (YAML `---` or TOML `+++` front
matter; JSON unsupported) plus optional sibling companions:
`linkedin-post.txt` (full text becomes the LinkedIn `commentary`, ≤3000 UTF-16
units) and `x-post.txt` (posted verbatim as the tweet, ≤280 weighted chars,
URLs count 23). The `status` command's `classify` function maps each post to
one of: `future`/`draft`/`no companion`/`MISSING`/`scheduled`/`published`;
`x status`/`classifyX` does the same against `x-status.yaml` minus the
`scheduled` state. The `post` struct's `HasCompanion` refers to the LinkedIn
companion — the X report re-derives it from `x-post.txt` (`hasXCompanion`).

### Publish payload & previews

`publish` sends to the LinkedIn Posts API (`linkedin.go`). If the post's `date:`
(or `--at`) is in the future it sets `publishedAt` (epoch millis) to **schedule**;
if past it publishes immediately with a warning. The Posts API does **not** scrape
`og:image`, so the card thumbnail is uploaded from the bundle's
`featured.{jpg,png}` via the Images API (`uploadImage`) and set as
`content.article.thumbnail` (JPEG/PNG only — WebP is rejected). The article link
is `<LISYNC_BASE_URL>/posts/<slug>/` (default `https://carlos.enredando.me`,
override via `LISYNC_BASE_URL`). `--dry-run` prints the payload with a
placeholder author URN and needs no auth.

### X specifics

- **OAuth 2.0 + PKCE only** (S256). The client secret is optional: "Native App"
  type apps are public clients (PKCE alone); "Web App" type are confidential
  (Basic auth at the token endpoint). `resolveXAuthCredentials` accepts an
  ID-only credential everywhere; don't reintroduce LinkedIn's both-or-neither
  rule.
- **Refresh tokens are single-use** (rotated on every refresh).
  `ensureFreshXTokens` persists the rotated store BEFORE returning; a persist
  failure is a hard error — never proceed with an un-persisted rotated token.
  Access tokens last ~2h.
- **No scheduling** via the API → `x publish` refuses future-dated posts.
  **No edit endpoint** → only `x republish` (delete + repost).
- **No image upload**: X scrapes the link card from the URL in the tweet text
  (the blog emits `twitter:card` meta), so `x-post.txt` must contain the
  article URL (warned if absent). The LinkedIn `verifyArticleOG` preflight is
  reused as-is.
- `tweetLen` is an approximation of X's weighting (whitespace-tokenized URL
  detection); the API is the authority and `postTweet` surfaces its errors.

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
