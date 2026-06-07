# li-sync

Audit which posts of a Hugo blog have been queued or published on LinkedIn and,
optionally, publish them via the LinkedIn API. It can also post the companion
tweet to **X (Twitter)** — see [X (Twitter)](#x-twitter).

Two modes coexist:
- **Manual mode** (zero setup): you schedule posts in LinkedIn's native composer,
  then run `mark` to record it. Use this if you don't want to deal with OAuth.
- **API mode** (one-time setup): you authorize a LinkedIn app once (`auth`),
  then `publish` posts directly from the CLI — either immediately or scheduled
  for the post's date.

The tool lives outside any specific blog repo. It points at a Hugo site root and
operates on `content/posts/<slug>/index.md` + a sibling `linkedin-post.txt`
companion, plus a versioned state file (`linkedin-status.yaml`) at the site root.

## Why

Convention: every post under `content/posts/<slug>/` ships with a
`linkedin-post.txt` companion that gets published to LinkedIn around the same
date as the blog post. Without tracking, posts drift unpublished. `li-sync`
reconciles `content/posts/` against `linkedin-status.yaml` and tells you what's
outstanding.

## Build

```
go build -o li-sync .
```

For a system-wide install:

```
go install github.com/carlosprados/li-sync@latest
```

The binary is gitignored.

## Development

This repo uses [Task](https://taskfile.dev) (`go-task`) for build, formatting,
and quality gates. Install it with
`go install github.com/go-task/task/v3/cmd/task@latest`, then:

```
task              # list available tasks
task build        # build the li-sync binary
task run -- status --all   # build and run with args
task fmt          # gofmt -w .
task check        # fmt check + go vet + build + go test (run before pushing)
task tidy         # go mod tidy && go mod verify
task release:snapshot      # local GoReleaser snapshot (needs goreleaser installed)
```

Releases are tag-driven: pushing a `v*` tag triggers GoReleaser via GitHub
Actions (`.github/workflows/release.yml`); no manual release steps.

## Pointing li-sync at a Hugo repo

`li-sync` is a self-contained binary. It discovers the Hugo site root via this
precedence:

1. `--repo <path>` flag.
2. `LISYNC_REPO` environment variable.
3. Walk up from the current working directory until a `content/posts/` is found.

So all of these work:

```
li-sync --repo ~/sites/carlos.enredando.me status
LISYNC_REPO=~/sites/carlos.enredando.me li-sync status
cd ~/sites/carlos.enredando.me && li-sync status
```

## Commands

The CLI is built with [Cobra](https://github.com/spf13/cobra) and
[Viper](https://github.com/spf13/viper): every command and flag is
self-documenting via `li-sync <command> --help` (or `li-sync help <command>`),
and configuration is resolved from flags, env vars, or a config file (see
[Configuration summary](#configuration-summary)).

### `status`

```
li-sync status [--all]
```

Lists posts with their LinkedIn state. By default hides rows that are not
actionable (future posts, drafts, posts without a companion `linkedin-post.txt`).
Use `--all` to see everything.

States:
- `MISSING` — post is published in Hugo (`date <= now`, not draft, has companion)
  but not registered in `linkedin-status.yaml`. **Action: schedule it in
  LinkedIn and run `mark`.**
- `scheduled` — registered as queued in LinkedIn for a future datetime.
- `published` — registered as already posted.
- `future` — post `date:` is in the future; ignore for now.
- `draft` — `draft: true` in front matter; ignore until published.
- `no companion` — post has no `linkedin-post.txt`. Old posts (pre-convention)
  typically don't.

### `mark`

```
li-sync mark <slug> --at <datetime> [--published] [--note "text"]
```

Register a post as scheduled (default) or already published. The datetime
accepts:
- RFC3339: `2026-05-20T08:30:00+02:00`
- `YYYY-MM-DDTHH:MM:SS` (assumed local)
- `YYYY-MM-DD HH:MM` (assumed local)
- `YYYY-MM-DD` (assumed local midnight)

### `unmark`

```
li-sync unmark <slug>
```

Removes the post's entry from `linkedin-status.yaml`. Use if you marked
something by mistake.

### `open`

```
li-sync open <slug>
```

Opens the post's `linkedin-post.txt` in `$EDITOR` (fallback `nvim`) and then
opens the LinkedIn share composer in the default browser. After scheduling on
LinkedIn, run `mark` to record it.

### `auth`

```
li-sync auth [--client-id ID --client-secret SECRET]
```

One-time OAuth flow. Opens browser to LinkedIn's authorization page, receives
the callback on `http://localhost:8765/callback`, exchanges the code for an
access token + refresh token, and persists them under
`$XDG_CONFIG_HOME/li-sync/tokens.json` (mode 0600).

Credentials are resolved with this precedence:

1. **CLI flags** `--client-id` + `--client-secret` (must be provided together).
   The values are saved to `app.json` (chmod 0600) so subsequent runs don't
   need them.
2. **Env vars** `LINKEDIN_CLIENT_ID` + `LINKEDIN_CLIENT_SECRET`. Used as-is,
   **not** persisted (env is intentionally ephemeral).
3. **`app.json`** in the config dir if present.
4. **Interactive prompt**: if none of the above is set, the tool asks for the
   Client ID (visible) and Client Secret (input hidden via `golang.org/x/term`).
   The answers are saved to `app.json`.

See **Setup for API mode** below for the one-time LinkedIn Developer Portal
setup.

### `publish`

```
li-sync publish <slug> [--at <datetime>] [--force] [--dry-run] [--no-verify]
```

Publishes (or schedules) the companion to LinkedIn via API.

- Without `--at`: uses the post's `date:` from front matter. If that date is in
  the future, the post is **scheduled** on LinkedIn (`publishedAt` epoch
  millis). If it's in the past, publishes immediately with a warning.
- `--at <datetime>`: override the schedule/publish moment. Accepts the same
  formats as `mark`.
- `--force`: republish even if the slug already has an entry in
  `linkedin-status.yaml`.
- `--dry-run`: run the preflight and print the JSON payload that would be sent
  (with placeholder author URN) without calling the API. Doesn't need `auth`.
- `--no-verify`: skip the preflight (not recommended).

**Preflight (always on unless `--no-verify`).** Before creating the post,
`li-sync` fetches the article page and refuses to publish unless it returns
HTTP 200 **and** exposes a reachable `og:image`. LinkedIn snapshots the link
card at creation time and caches the Open Graph data per URL, so publishing
against a not-yet-deployed page bakes a permanently broken, imageless card that
only a delete + re-create can fix. Always `--dry-run` first.

The article URL is `<base>/posts/<url-slug>/`, where `<url-slug>` is the post's
front-matter `slug:` if set, otherwise the directory name — matching what Hugo
publishes (so posts with a custom `slug:` link correctly).

On success the tool auto-marks the post in `linkedin-status.yaml` with
`status: scheduled` or `published` and stores the LinkedIn post URN as the
note.

**Article card image.** The Posts API does **not** scrape the page's `og:image`,
so `li-sync` uploads the bundle's `featured.{jpg,png}` via the Images API and
sets it as the card thumbnail. Use JPEG or PNG (LinkedIn rejects WebP for
upload). If the post directory has no `featured` image, the card is published
without a picture (with a warning). The base URL used to build the article link
defaults to `https://carlos.enredando.me`; override with `--base-url`, the
`LISYNC_BASE_URL` env var, or the config file for other Hugo sites.

### `edit`

```
li-sync edit <slug>
```

Updates an already-published post's **commentary** (text) on LinkedIn from the
current `linkedin-post.txt`, via a `PARTIAL_UPDATE`. Requires the slug to have a
recorded URN (i.e. it was published with this tool). The LinkedIn API only
allows editing the commentary — the article **card/image cannot** be changed in
place; use `republish` for that.

### `republish`

```
li-sync republish <slug> [--at <datetime>] [--no-verify]
```

Deletes the existing LinkedIn post (by its recorded URN) and creates a fresh one
with the full publish preflight, recording the new URN. This is the only way to
change a published post's article card — for example after fixing the page's
`og:image`. The preflight still refuses to re-create the post if the article
page isn't live, so a transient deploy gap can't strand you with no post.

### `tui`

```
li-sync tui
```

Opens an interactive terminal dashboard: a navigable table of every post with
its LinkedIn state, a live preview of the selected post's companion, and the
common write actions behind an explicit confirmation.

This is an **additive** front-end — it never becomes the only way to do
anything. Every action it offers calls the *same* core functions as the CLI
subcommands, which remain the primary, fully scriptable path. The `tui` command
requires a TTY; in a non-interactive context (CI, an automated agent) it exits
with a clear error, so use the subcommands there.

If no repo is resolved (no `--repo`/`LISYNC_REPO` and `content/posts/` isn't in
an ancestor of the cwd), the TUI opens a **directory picker** so you can choose
your Hugo site root interactively, instead of erroring out. Press `c` from the
table to switch repos at any time. A picked directory is validated — it must
contain `content/posts/` — and rejected inline if not.

Keys:
- `↑`/`↓` move · `a` toggle all/actionable · `r` reload · `c` change repo · `q` quit
- `d` dry-run · `p` publish · `e` edit · `R` republish · `u` unmark
- picker: `↑`/`↓` move · `enter` choose dir · `h`/`esc` up a level · `ctrl+c` quit

Write actions (`p`/`e`/`R`/`u`) prompt for `y`/`n` confirmation first, then run
asynchronously with live progress (preflight, thumbnail upload, …) and show the
result. `d` (dry-run) needs no auth and runs the preflight without posting.
Scheduling with a custom datetime (`mark --at`, `publish --at`) stays CLI-only.

### Mentions

Write `{{@Display Name}}` anywhere in a `linkedin-post.txt`. On publish/edit it
expands to LinkedIn mention syntax `@[Display Name](urn)` using a `mentions` map
resolved by Viper — put it in the config file:

```yaml
# $XDG_CONFIG_HOME/li-sync/config.yaml
mentions:
  "Amplía Soluciones": "urn:li:organization:123456"
  "Antonio Gulli": "urn:li:person:abcd1234"
```

The text rendered is the name you wrote; the lookup is case-insensitive. An
unconfigured name is left as plain text with a warning. (You can also write the
raw `@[Name](urn)` syntax directly — it passes through.)

## X (Twitter)

The `x` command tree mirrors the LinkedIn commands for X:

```
li-sync x auth                      # one-time OAuth 2.0 + PKCE flow (API mode)
li-sync x status [--all]            # posts and their X state
li-sync x open <slug>               # manual mode: browser composer, tweet pre-filled
li-sync x publish <slug> [--dry-run | --force | --no-verify]
li-sync x republish <slug>          # delete the tweet + post a fresh one
li-sync x mark <slug> [--at <dt>] [--id <tweet-url-or-id>]
li-sync x unmark <slug>
```

Like LinkedIn, two modes coexist:

- **Manual mode** (zero setup, zero cost): `x open` opens X's web intent in the
  browser with the tweet text from `x-post.txt` **already filled in** — you just
  press Post, then record it with `x mark <slug> --id <tweet-url-or-id>`. No
  developer account, no OAuth, no per-tweet fee.
- **API mode** (one-time setup, pay-per-use): `x auth` once, then `x publish`
  tweets directly with preflight and automatic state recording.

Each post may have an **`x-post.txt`** companion (sibling of
`linkedin-post.txt`) holding the tweet text, posted **verbatim** — no mention
expansion (`@handles` are plain text on X). State is tracked in
**`x-status.yaml`** at the Hugo site root, separate from
`linkedin-status.yaml`, same schema (status is only ever `published`; the note
holds the tweet ID).

Key differences from LinkedIn:

- **No scheduling.** The X API v2 cannot schedule tweets (only the UI/Ads API
  can), so `x publish` **refuses future-dated posts** — run it on/after the
  post's date. `x status` shows what's due as `MISSING`.
- **No edit.** The API has no edit endpoint; `x republish` (delete + repost) is
  the only way to change a published tweet.
- **No image upload.** X scrapes the link card from the URL in the tweet text
  (the page's `twitter:card`/`og:` meta), so `x-post.txt` should contain the
  article URL — `x publish` warns if it doesn't. The same preflight as LinkedIn
  (page live + reachable `og:image`) runs before tweeting.
- **280 weighted characters.** URLs count as 23 regardless of length (t.co).
  `li-sync` pre-checks with an approximation of X's weighting; the API is the
  final authority.

### Setup for X (one-time)

> **Cost note.** Since February 2026 the X API has no free tier for new
> developers: the default is pay-per-use at **$0.01 per tweet created**, billed
> to your developer account. At a weekly cadence this is negligible, but a
> developer account with billing is required.

1. Create a project + app at https://developer.x.com/en/portal/projects-and-apps.
2. In the app's **User authentication settings**, enable OAuth 2.0:
   - **Type of App**: "Web App, Automated App or Bot" (confidential client,
     recommended — you get a client secret) or "Native App" (public client, no
     secret; PKCE alone).
   - **Callback URI**: `http://localhost:8765/callback` (verbatim).
   - **Website URL**: your blog.
3. Copy the **OAuth 2.0 Client ID** (and **Client Secret** if confidential)
   from Keys and tokens.
4. Run `li-sync x auth` once. Credentials resolve like LinkedIn's:
   `--client-id`/`--client-secret` flags (saved to `x_app.json`) >
   `X_CLIENT_ID`/`X_CLIENT_SECRET` env (not saved) > `x_app.json` > interactive
   prompt. The secret is optional (public clients have none).
5. A browser tab opens, you grant the scopes (`tweet.read`, `tweet.write`,
   `users.read`, `offline.access`), and tokens are saved to
   `$XDG_CONFIG_HOME/li-sync/x_tokens.json` (0600).

Access tokens last ~2 hours; the tool auto-refreshes. X **rotates refresh
tokens on every refresh** (single-use), so `x_tokens.json` is rewritten each
time — if a refresh ever fails mid-rotation, re-run `li-sync x auth`.

## Setup for API mode (one-time)

1. Register an app at https://www.linkedin.com/developers/apps. Standalone app
   is fine; LinkedIn will ask you to associate it with a Company Page (a dummy
   page is acceptable).
2. Add products: at least **"Sign In with LinkedIn using OpenID Connect"** +
   **"Share on LinkedIn"** (the one that grants `w_member_social`).
3. Under "Auth" → **Authorized redirect URLs for your app**, add:
   ```
   http://localhost:8765/callback
   ```
4. Copy the Client ID and Primary Client Secret (Auth tab).
5. Run `li-sync auth` once. You can pass the credentials any of these ways:
   ```
   # Option A: flags (saved to app.json automatically)
   li-sync auth --client-id <id> --client-secret <secret>

   # Option B: env vars (not saved)
   LINKEDIN_CLIENT_ID=... LINKEDIN_CLIENT_SECRET=... li-sync auth

   # Option C: just run it; you'll be prompted (Client Secret input is hidden)
   li-sync auth
   ```
   A browser tab opens, you grant the requested scopes (`openid`, `profile`,
   `email`, `w_member_social`), the tab returns to localhost, and the binary
   saves your tokens.
6. From now on, `publish` works. Access tokens expire after ~60 days but the
   tool auto-refreshes them silently (refresh tokens last ~365 days). When the
   refresh token finally expires, re-run `auth`.

## Troubleshooting `auth`

### "The redirect_uri does not match the registered value"

LinkedIn shows this in the browser *before* you reach the consent screen. It
means the redirect URL `li-sync` sends is not registered (verbatim) in your app.
`li-sync` always sends exactly:

```
http://localhost:8765/callback
```

Fix: portal → your app → **Auth** tab → **OAuth 2.0 settings** → **Authorized
redirect URLs for your app** → add that URL. The most common cause is the list
being **empty** ("No redirect URLs added"). Watch for non-exact matches that
also fail: `https://` instead of `http://`, a trailing slash, `127.0.0.1`
instead of `localhost`, or a different port.

This is unrelated to scopes or products — those would fail *after* the callback,
not before it.

### Authorization succeeds but `publish` later fails with a permissions error

Check the **Products** tab has **"Sign In with LinkedIn using OpenID Connect"**
(grants `openid profile email`) and **"Share on LinkedIn"** (grants
`w_member_social`). The **Auth** tab's "OAuth 2.0 scopes" list should show all
four. If publishing is rejected, the app may also need to be verified against
its associated Company Page (Settings tab → "Verify").

## Typical workflows

### API mode (after `auth` is done)

1. Merge a post to `main`. CF Pages deploys it.
2. Run `li-sync status` → see the slug listed as `MISSING`.
3. Run `li-sync publish <slug> --dry-run` to sanity-check the payload.
4. Run `li-sync publish <slug>` — uses the post's `date:`; schedules in
   LinkedIn if future, publishes if past. State file is updated automatically.
5. Commit `linkedin-status.yaml` in the Hugo repo.

> **Link in first comment.** LinkedIn's feed favours posts without an outbound
> link in the body, so the usual tactic is to drop the article URL in the first
> comment. `li-sync` does **not** post that comment for you — the comment
> (Social Actions) API requires a separate product grant ("Community Management
> API") that the default "Share on LinkedIn" app does not have, and requesting
> it returns `403 ACCESS_DENIED`. Add the link as the first comment by hand in
> the LinkedIn UI after publishing.

### Manual mode (no API setup)

1. Merge a post to `main`. CF Pages deploys it.
2. Run `li-sync status` → see the slug listed as `MISSING`.
3. Run `li-sync open <slug>` → companion opens in editor, LinkedIn composer
   opens in browser.
4. Copy/paste content into composer, schedule the post (clock icon → date/time
   → Schedule).
5. Run `li-sync mark <slug> --at 2026-05-20T09:00:00+02:00`.
6. Commit `linkedin-status.yaml` in the Hugo repo.

For posts already published before adopting this tool: run
`mark <slug> --published --at <approx date>` once per post. From then on the
state is canonical.

### X: manual mode (zero setup, zero cost)

1. The post's date arrives and the page is live.
2. Run `li-sync x status` → see the slug listed as `MISSING`.
3. Run `li-sync x open <slug>` → browser opens X's composer with the tweet
   from `x-post.txt` already filled in. Press **Post**.
4. Copy the posted tweet's link (share → Copy link) and run
   `li-sync x mark <slug> --id <pasted-url>` — the tweet ID is extracted
   from the URL (a bare ID works too).
5. Commit `x-status.yaml` in the Hugo repo.

### X: API mode (after `x auth` is done)

1. The post's date arrives and the page is live (`x publish` refuses
   future-dated posts — the API cannot schedule).
2. Run `li-sync x status` → see the slug listed as `MISSING`.
3. Run `li-sync x publish <slug> --dry-run` to see the exact tweet and its
   weighted length.
4. Run `li-sync x publish <slug>` — preflight, tweet, and `x-status.yaml`
   updated with the tweet ID automatically.
5. Commit `x-status.yaml` in the Hugo repo.

## End-to-end example: one post, both networks

The full life of a post, from writing to both networks recorded. Assume a Hugo
site at `~/blog` and a new post for **2026-07-21**.

**1. Write the post bundle** — three files next to `index.md`:

```
content/posts/agentic-ai-ch11-goal-setting/
├── index.md            # the blog post (front-matter date: 2026-07-21)
├── featured.jpg        # used as the LinkedIn card thumbnail (uploaded via API)
├── linkedin-post.txt   # LinkedIn companion (≤3000 UTF-16 units)
└── x-post.txt          # X companion (≤280 weighted chars, URLs count 23)
```

An `x-post.txt` is short and must **contain the article URL** (that's what X
renders the link card from):

```
Agents that drift off-goal are agents you can't ship.

Chapter 11 of my Agentic Design Patterns series: Goal Setting and
Monitoring — explicit objectives, measurable progress, course correction.

https://carlos.enredando.me/posts/agentic-ai-ch11-goal-setting/
```

**2. Before the date — LinkedIn can schedule ahead, X cannot:**

```
$ li-sync status
agentic-ai-ch11-goal-setting  2026-07-21  MISSING  schedule it in LinkedIn

$ li-sync publish agentic-ai-ch11-goal-setting --dry-run   # sanity-check payload
$ li-sync publish agentic-ai-ch11-goal-setting
scheduled agentic-ai-ch11-goal-setting for 2026-07-21 (URN: urn:li:share:7350...)

$ li-sync x status --all
agentic-ai-ch11-goal-setting  2026-07-21  future   publish on/after 2026-07-21
```

LinkedIn is now queued. The X row stays `future` — nothing to do yet.

**3. On/after 2026-07-21 — the page is live, post to X.** Pick one mode:

```
# Manual (no developer account):
$ li-sync x open agentic-ai-ch11-goal-setting       # browser opens, tweet pre-filled
# ... press Post, grab the tweet ID from the URL ...
$ li-sync x mark agentic-ai-ch11-goal-setting --id https://x.com/cprados/status/1947231598234177536

# API (after one-time `x auth`):
$ li-sync x publish agentic-ai-ch11-goal-setting --dry-run
$ li-sync x publish agentic-ai-ch11-goal-setting
preflight OK: https://carlos.enredando.me/posts/agentic-ai-ch11-goal-setting/ is live, og:image ... reachable
posted agentic-ai-ch11-goal-setting to X (tweet ID: 1947231598234177536)
```

**4. Verify and persist the state** — both files live at the Hugo site root and
are versioned in the blog repo:

```
$ li-sync status          # → published (after LinkedIn fires the schedule)
$ li-sync x status        # → published  2026-07-21  // 1947231598234177536
$ cd ~/blog && git add linkedin-status.yaml x-status.yaml && git commit -m "chore: record ch11 on LinkedIn + X"
```

**5. (LinkedIn only) add the article link as the first comment by hand** — see
the note in the LinkedIn workflow above.

Done: one bundle, two networks, all state versioned next to the content.

## State file

`linkedin-status.yaml` at the Hugo site root. Versioned in the Hugo repo so it
survives across machines. Schema:

```yaml
posts:
  <slug>:
    scheduled_for: 2026-05-20T09:00:00+02:00
    status: scheduled    # or "published"
    note: "optional"
```

Edit by hand only if you know what you're doing — easier to use `mark`/`unmark`.

## Configuration summary

| What                       | Source                                                          | Default                       |
|----------------------------|-----------------------------------------------------------------|-------------------------------|
| Hugo site root             | `--repo`, `LISYNC_REPO`, cwd walk-up                            | —                             |
| Article base URL           | `LISYNC_BASE_URL`                                               | `https://carlos.enredando.me` |
| OAuth config/tokens dir    | `LI_SYNC_CONFIG_DIR`                                            | `$XDG_CONFIG_HOME/li-sync/`   |
| Last repo opened in `tui`  | tool-managed `last_repo` file in the config dir (`tui` only)    | —                             |
| LinkedIn app credentials   | `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET`, or `app.json`   | —                             |
| X app credentials          | `X_CLIENT_ID`, `X_CLIENT_SECRET`, or `x_app.json`               | —                             |
| X OAuth tokens             | tool-managed `x_tokens.json` in the config dir                  | —                             |

## Limitations

- Reads Hugo YAML (`---`) and TOML (`+++`) front matter. JSON front matter is
  not supported.
- `mark`/`unmark` are trust-based: the state file is what you tell it.
  `publish` updates it automatically when the API call succeeds.
- The `open` command on Linux requires `xdg-open` for the browser; on macOS
  `open`; on Windows `rundll32`.
- The OAuth callback server listens on TCP `:8765`. If you have something
  running on that port at `auth` time, the command fails fast — close the
  conflicting process and retry.
- `auth` flow times out after 5 minutes. If the browser flow takes longer,
  re-run.
- `publish --dry-run` shows the payload with a placeholder author URN. For a
  real run, the URN is read from `tokens.json` (populated by `auth`).

## License

Apache 2.0 — see [LICENSE](LICENSE).
