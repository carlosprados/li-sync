# li-sync

Audit which posts of a Hugo blog have been queued or published on LinkedIn and,
optionally, publish them via the LinkedIn API.

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

The article preview is generated by LinkedIn from the `og:*` meta tags of the
article URL — no manual image upload. The base URL defaults to
`https://carlos.enredando.me`; override with `--base-url`, the `LISYNC_BASE_URL`
env var, or the config file for other Hugo sites.

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
| LinkedIn app credentials   | `LINKEDIN_CLIENT_ID`, `LINKEDIN_CLIENT_SECRET`, or `app.json`   | —                             |

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
