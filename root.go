package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "li-sync",
		Short: "Audit and publish which blog posts are queued/published on LinkedIn",
		Long: `li-sync — track which blog posts have been queued or published on LinkedIn.

A sidecar to the LinkedIn post scheduler. It audits a Hugo repo against a
versioned state file (linkedin-status.yaml) and can publish or schedule a
post's companion (linkedin-post.txt) to LinkedIn via the API after a one-time
OAuth flow (see "li-sync auth").

Repo discovery (Viper precedence): --repo flag > LISYNC_REPO env > config file >
walk up from cwd until a directory containing content/posts/ is found.

Config file: $XDG_CONFIG_HOME/li-sync/config.yaml may set keys "repo",
"base_url", "client_id", "client_secret". Env vars LISYNC_REPO, LISYNC_BASE_URL,
LINKEDIN_CLIENT_ID, LINKEDIN_CLIENT_SECRET override the file. OAuth tokens are
stored in $XDG_CONFIG_HOME/li-sync/tokens.json, never in the repo.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String("repo", "", "path to the Hugo site root (dir containing content/posts/)")
	root.PersistentFlags().String("base-url", "", "base URL for article preview links (default "+defaultSiteBaseURL+")")

	cobra.OnInitialize(func() { initConfig(root) })

	root.AddCommand(
		newStatusCmd(),
		newMarkCmd(),
		newUnmarkCmd(),
		newOpenCmd(),
		newAuthCmd(),
		newPublishCmd(),
		newEditCmd(),
		newRepublishCmd(),
		newTUICmd(),
		newXCmd(),
	)
	return root
}

// initConfig wires Viper: persistent flags, env vars, and an optional config
// file. Runs (via cobra.OnInitialize) after flag parsing, before the command.
func initConfig(root *cobra.Command) {
	_ = viper.BindPFlag("repo", root.PersistentFlags().Lookup("repo"))
	_ = viper.BindPFlag("base_url", root.PersistentFlags().Lookup("base-url"))

	viper.SetDefault("base_url", defaultSiteBaseURL)

	viper.SetEnvPrefix("LISYNC")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	_ = viper.BindEnv("repo", "LISYNC_REPO")
	_ = viper.BindEnv("base_url", "LISYNC_BASE_URL")
	_ = viper.BindEnv("client_id", "LINKEDIN_CLIENT_ID")
	_ = viper.BindEnv("client_secret", "LINKEDIN_CLIENT_SECRET")
	_ = viper.BindEnv("x_client_id", "X_CLIENT_ID")
	_ = viper.BindEnv("x_client_secret", "X_CLIENT_SECRET")

	if dir, err := configDir(); err == nil {
		viper.AddConfigPath(dir)
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		_ = viper.ReadInConfig() // config file is optional; ignore "not found"
	}
}

// repoRoot resolves the Hugo site root from Viper (flag/env/config) or discovery.
func repoRoot() (string, error) {
	return resolveRepoRoot(viper.GetString("repo"))
}

const defaultSiteBaseURL = "https://carlos.enredando.me"

// siteBaseURL resolves the article base URL from Viper (--base-url flag /
// LISYNC_BASE_URL env / config file), falling back to the built-in default so
// the tool can serve other Hugo sites without a rebuild.
func siteBaseURL() string {
	if v := strings.TrimRight(viper.GetString("base_url"), "/"); v != "" {
		return v
	}
	return defaultSiteBaseURL
}

// configuredMentions returns the {{@Name}} → URN map from Viper. Viper
// lowercases the keys, which is what applyMentions expects for its
// case-insensitive lookup.
func configuredMentions() map[string]string {
	return viper.GetStringMapString("mentions")
}

// cliReporter sends progress steps to stderr, reproducing the exact progress
// output li-sync printed before the publish logic was decoupled from the CLI.
func cliReporter() Reporter { return newWriterReporter(os.Stderr) }

// printPublishResult renders a PublishResult to stdout, matching the CLI output
// the publish/republish commands produced before they returned data instead of
// printing inline.
func printPublishResult(res PublishResult) {
	if res.DryRun {
		encoded, _ := json.MarshalIndent(res.Payload, "", "  ")
		fmt.Println("--- payload (dry run) ---")
		fmt.Println(string(encoded))
		fmt.Println("--- end payload ---")
		if res.Scheduled {
			fmt.Printf("would schedule for %s\n", formatDateTime(res.PublishAt))
		} else {
			fmt.Println("would publish immediately")
		}
		return
	}
	if res.Scheduled {
		fmt.Printf("scheduled %s for %s (URN: %s)\n", res.Slug, formatDateTime(res.PublishAt), res.URN)
	} else {
		fmt.Printf("published %s (URN: %s)\n", res.Slug, res.URN)
	}
}

func newStatusCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List every post and its LinkedIn state",
		Long: `List every post and its LinkedIn state.

Scans content/posts/, cross-references linkedin-status.yaml, and prints a table
(SLUG, POST DATE, LINKEDIN STATE, ACTION). States:
  published     already on LinkedIn (recorded)
  scheduled     queued on LinkedIn for a future time (recorded)
  MISSING       date has passed, has a companion, not yet on LinkedIn
  future        post date is in the future (hidden unless --all)
  draft         draft:true (hidden unless --all)
  no companion  no linkedin-post.txt (hidden unless --all)`,
		Example: "  li-sync status\n  li-sync status --all\n  li-sync --repo ~/blog status",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runStatus(root, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show all rows (default hides future, draft, no-companion)")
	return cmd
}

func newMarkCmd() *cobra.Command {
	var at, note string
	var published bool
	cmd := &cobra.Command{
		Use:   "mark <slug>",
		Short: "Record a post's LinkedIn state in linkedin-status.yaml (no API call)",
		Long: `Record a post's LinkedIn state in linkedin-status.yaml (trust-based, no API call).

For posts you scheduled or published by hand in LinkedIn's composer. Use the
"publish" command if you want li-sync to post via the API instead.`,
		Example: "  li-sync mark why-keystone --published --at 2026-02-16\n" +
			"  li-sync mark agentic-ai-ch04-reflection --at 2026-06-02T12:00:00+02:00\n" +
			"  li-sync mark cli-built-for-the-ai --note \"urn:li:share:123\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runMark(root, args[0], at, published, note)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "datetime (RFC3339 or YYYY-MM-DD[ HH:MM]) when posted; required for scheduled entries")
	cmd.Flags().BoolVar(&published, "published", false, "mark as already published (default new state is scheduled)")
	cmd.Flags().StringVar(&note, "note", "", "optional free-form note (e.g. the post URN)")
	return cmd
}

func newUnmarkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unmark <slug>",
		Short: "Remove a post's entry from linkedin-status.yaml",
		Long: `Remove a post's entry from linkedin-status.yaml, reverting it to unscheduled.

Use before re-publishing a post whose LinkedIn post you deleted, so "publish"
won't refuse with "already recorded".`,
		Example: "  li-sync unmark cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runUnmark(root, args[0])
		},
	}
}

func newOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <slug>",
		Short: "Open the companion in $EDITOR and the LinkedIn composer in the browser",
		Long: `Open the companion in $EDITOR and the LinkedIn composer in the browser.

The manual, no-API publishing path. Opens linkedin-post.txt in $EDITOR
(default nvim) so you can copy it, and opens the LinkedIn share composer. After
scheduling there, record it with: li-sync mark <slug> --at <datetime>`,
		Example: "  li-sync open cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runOpen(root, args[0])
		},
	}
}

func newAuthCmd() *cobra.Command {
	var clientID, clientSecret string
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "One-time OAuth flow so li-sync can post on your behalf",
		Long: `One-time OAuth flow so li-sync can post on your behalf.

Opens the browser to LinkedIn's consent page, runs a local callback on
http://localhost:8765/callback, and saves tokens to
$XDG_CONFIG_HOME/li-sync/tokens.json (0600). Scopes: openid, profile, email,
w_member_social. The LinkedIn app's "Authorized redirect URLs" must list
http://localhost:8765/callback verbatim (no trailing slash, no 127.0.0.1).

Credential precedence: --client-id/--client-secret flags (saved to app.json) >
LINKEDIN_CLIENT_ID + LINKEDIN_CLIENT_SECRET env / config file > interactive
prompt (secret hidden).`,
		Example: "  li-sync auth\n  li-sync auth --client-id 86abcd --client-secret s3cr3t",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := clientID
			if id == "" {
				id = viper.GetString("client_id")
			}
			secret := clientSecret
			if secret == "" {
				secret = viper.GetString("client_secret")
			}
			return runAuthFlow(id, secret)
		},
	}
	cmd.Flags().StringVar(&clientID, "client-id", "", "LinkedIn app Client ID (saved to app.json)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "LinkedIn app Client Secret (saved to app.json)")
	return cmd
}

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit <slug>",
		Short: "Update an already-published post's text from its current companion",
		Long: `Update an already-published post's commentary (text) on LinkedIn from the
current linkedin-post.txt, via a PARTIAL_UPDATE. Requires the slug to have a
recorded URN (i.e. it was published with this tool).

The LinkedIn API only allows editing the commentary — the article card and its
image CANNOT be changed in place. To replace the card (e.g. after fixing the
page's Open Graph image), use "republish" instead.`,
		Example: "  li-sync edit cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			urn, err := runEdit(root, args[0], configuredMentions())
			if err != nil {
				return err
			}
			fmt.Printf("edited %s commentary (URN: %s)\n", args[0], urn)
			return nil
		},
	}
}

func newRepublishCmd() *cobra.Command {
	var at string
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "republish <slug>",
		Short: "Delete the existing LinkedIn post and create a fresh one (refreshes the card)",
		Long: `Delete the existing LinkedIn post (by its recorded URN) and create a fresh one
with the full publish preflight, recording the new URN.

This is the only way to change a published post's article card — editing
commentary in place cannot. Use it after fixing the page's Open Graph image or
when the companion changed substantially. The preflight still refuses to
re-create the post if the article page is not live with a reachable og:image,
so a transient deploy gap can't strand you with no post.`,
		Example: "  li-sync republish cli-built-for-the-ai\n" +
			"  li-sync republish agentic-ai-ch03-parallelization --at 2026-05-26",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			res, err := runRepublish(root, args[0], at, noVerify, siteBaseURL(), configuredMentions(), cliReporter())
			if err != nil {
				return err
			}
			printPublishResult(res)
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "override publish/schedule datetime for the new post (default: post's front-matter date)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article/og:image preflight (not recommended)")
	return cmd
}

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive read-only dashboard for browsing post/LinkedIn state",
		Long: `Open an interactive terminal dashboard: a navigable table of every post with
its LinkedIn state, plus a live preview of the selected post's companion.

This is an ADDITIVE front-end and is strictly read-only — it performs no API
calls and writes nothing. Every mutating action (publish, mark, edit, …) lives
in the CLI subcommands, which stay fully scriptable without a terminal. The tui
command requires a TTY; automated callers should use the subcommands instead.

Keys: ↑/↓ move · a toggle all/actionable · r reload · q quit.`,
		Example: "  li-sync tui\n  li-sync --repo ~/blog tui",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A missing/unresolvable repo is fine here: the TUI opens a
			// directory picker so you can choose the Hugo root interactively.
			root, _ := repoRoot()
			last := loadLastRepo()
			// With nothing explicit, fall back to the last repo opened in the
			// TUI (if it's still a valid Hugo root).
			if root == "" && last != "" {
				if r, err := resolveRepoRoot(last); err == nil {
					root = r
				}
			}
			if root != "" {
				_ = saveLastRepo(root) // remember what we opened (best-effort)
			}
			return runTUI(root, siteBaseURL(), last)
		},
	}
}

func newPublishCmd() *cobra.Command {
	var at string
	var force, dryRun, noVerify bool
	cmd := &cobra.Command{
		Use:   "publish <slug>",
		Short: "Publish or schedule a post's companion to LinkedIn via the API",
		Long: `Publish or schedule a post's companion to LinkedIn via the API.

Reads the sibling linkedin-post.txt as the post body and the front-matter title
for the article card. The post's front-matter date decides timing: future →
scheduled, past → published immediately (override with --at). Requires a
completed "li-sync auth". On success, records the created URN in
linkedin-status.yaml.

Preflight (always on unless --no-verify): before creating the post, li-sync
fetches the article page and REFUSES to publish unless it returns HTTP 200 AND
exposes a reachable og:image. LinkedIn snapshots the link card at creation time
and caches the OG per URL, so publishing against a not-yet-deployed page bakes a
permanently-broken, imageless card. Always run --dry-run first.`,
		Example: "  li-sync publish cli-built-for-the-ai --dry-run\n" +
			"  li-sync publish cli-built-for-the-ai\n" +
			"  li-sync publish agentic-ai-ch04-reflection --at 2026-06-02T12:00:00+02:00",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			res, err := runPublish(root, args[0], at, force, dryRun, noVerify, siteBaseURL(), configuredMentions(), cliReporter())
			if err != nil {
				return err
			}
			printPublishResult(res)
			return nil
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "override publish/schedule datetime (default: post's front-matter date)")
	cmd.Flags().BoolVar(&force, "force", false, "publish even if the slug already has a state entry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run preflight + print payload, no API call (no auth needed)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article/og:image preflight (not recommended)")
	return cmd
}

// ---------- x (Twitter) command tree ----------

// printXPublishResult renders an XPublishResult to stdout, mirroring
// printPublishResult for the X command tree.
func printXPublishResult(res XPublishResult) {
	if res.DryRun {
		fmt.Println("--- tweet (dry run) ---")
		fmt.Println(res.Text)
		fmt.Println("--- end tweet ---")
		fmt.Printf("~%d weighted chars (limit %d) — would post immediately\n", tweetLen(res.Text), tweetMaxWeightedLen)
		return
	}
	fmt.Printf("posted %s to X (tweet ID: %s)\n", res.Slug, res.TweetID)
}

func newXCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "x",
		Short: "Publish blog posts to X (Twitter)",
		Long: `Publish blog posts to X (Twitter).

The X counterpart of the LinkedIn commands. Each post may have an x-post.txt
companion (sibling of linkedin-post.txt) holding the tweet text, posted
verbatim. State is tracked in x-status.yaml at the Hugo site root, separate
from linkedin-status.yaml.

Differences from LinkedIn:
  - No scheduling: the X API cannot schedule tweets. "x publish" refuses
    future-dated posts; run it on/after the post's date.
  - No edit: the X API has no edit endpoint; use "x republish" (delete+repost).
  - No thumbnail upload: X scrapes the link card from the URL in the tweet
    text, so x-post.txt should contain the article URL.`,
	}
	cmd.AddCommand(
		newXAuthCmd(),
		newXStatusCmd(),
		newXOpenCmd(),
		newXPublishCmd(),
		newXRepublishCmd(),
		newXMarkCmd(),
		newXUnmarkCmd(),
	)
	return cmd
}

func newXOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "open <slug>",
		Short: "Open X's composer in the browser with the tweet pre-filled (no API)",
		Long: `Open X's web intent composer in the browser with the post's x-post.txt
already filled in — the manual, zero-cost publishing path (no developer
account, no OAuth, no per-tweet fee).

You just press Post in the browser. Afterwards, record it with:
  li-sync x mark <slug> --note <tweet-id>

Warns (but still opens) if the text exceeds X's 280 weighted-character limit.`,
		Example: "  li-sync x open cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runXOpen(root, args[0])
		},
	}
}

func newXAuthCmd() *cobra.Command {
	var clientID, clientSecret string
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "One-time X OAuth 2.0 flow (Authorization Code + PKCE)",
		Long: `One-time X OAuth 2.0 flow so li-sync can tweet on your behalf.

Opens the browser to X's consent page, runs a local callback on
http://localhost:8765/callback, and saves tokens to
$XDG_CONFIG_HOME/li-sync/x_tokens.json (0600). Scopes: tweet.read, tweet.write,
users.read, offline.access. The X app's OAuth 2.0 settings must list
http://localhost:8765/callback as a Callback URI.

The client secret is optional: public clients ("Native App" type) use PKCE
alone; confidential clients ("Web App" type) also send the secret. X rotates
refresh tokens on every refresh — li-sync persists them automatically.

Credential precedence: --client-id/--client-secret flags (saved to x_app.json) >
X_CLIENT_ID + X_CLIENT_SECRET env / config file > interactive prompt.`,
		Example: "  li-sync x auth\n  li-sync x auth --client-id abc123",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			id := clientID
			if id == "" {
				id = viper.GetString("x_client_id")
			}
			secret := clientSecret
			if secret == "" {
				secret = viper.GetString("x_client_secret")
			}
			return runXAuthFlow(id, secret)
		},
	}
	cmd.Flags().StringVar(&clientID, "client-id", "", "X app OAuth 2.0 Client ID (saved to x_app.json)")
	cmd.Flags().StringVar(&clientSecret, "client-secret", "", "X app OAuth 2.0 Client Secret (optional — public clients have none)")
	return cmd
}

func newXStatusCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List every post and its X state",
		Long: `List every post and its X state.

Scans content/posts/, cross-references x-status.yaml, and prints a table
(SLUG, POST DATE, X STATE, ACTION). States:
  published     already on X (recorded)
  MISSING       date has passed, has x-post.txt, not yet on X
  future        post date is in the future — X cannot schedule (hidden unless --all)
  draft         draft:true (hidden unless --all)
  no companion  no x-post.txt (hidden unless --all)`,
		Example: "  li-sync x status\n  li-sync x status --all",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runXStatus(root, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show all rows (default hides future, draft, no-companion)")
	return cmd
}

func newXPublishCmd() *cobra.Command {
	var force, dryRun, noVerify bool
	cmd := &cobra.Command{
		Use:   "publish <slug>",
		Short: "Post a post's x-post.txt to X via the API",
		Long: `Post a post's x-post.txt companion to X via the API.

The companion is posted verbatim (no mention expansion — @handles are plain
text on X) and must fit X's 280 weighted-character limit (URLs count as 23).
It should contain the article URL: X scrapes the link card from it (the site's
twitter:card/og: meta), so no image upload happens.

There is NO scheduling: the X API cannot schedule tweets, so future-dated
posts are refused — run this on/after the post's date. Requires a completed
"li-sync x auth". On success, records the tweet ID in x-status.yaml.

Preflight (always on unless --no-verify): refuses to tweet unless the article
page returns HTTP 200 with a reachable og:image.`,
		Example: "  li-sync x publish cli-built-for-the-ai --dry-run\n" +
			"  li-sync x publish cli-built-for-the-ai",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			res, err := runXPublish(root, args[0], force, dryRun, noVerify, siteBaseURL(), cliReporter())
			if err != nil {
				return err
			}
			printXPublishResult(res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "post even if the slug already has an x-status.yaml entry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run checks + print the tweet, no API call (no auth needed)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article preflight (not recommended)")
	return cmd
}

func newXRepublishCmd() *cobra.Command {
	var noVerify bool
	cmd := &cobra.Command{
		Use:   "republish <slug>",
		Short: "Delete the existing tweet and post a fresh one",
		Long: `Delete the existing tweet (by its recorded ID) and post a fresh one from the
current x-post.txt, recording the new tweet ID.

X has no edit endpoint, so this is the only way to change a published tweet's
text or refresh its link card.`,
		Example: "  li-sync x republish cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			res, err := runXRepublish(root, args[0], noVerify, siteBaseURL(), cliReporter())
			if err != nil {
				return err
			}
			printXPublishResult(res)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article preflight (not recommended)")
	return cmd
}

func newXMarkCmd() *cobra.Command {
	var at, id string
	cmd := &cobra.Command{
		Use:   "mark <slug>",
		Short: "Record a post as published on X in x-status.yaml (no API call)",
		Long: `Record a post as published on X (trust-based, no API call).

For posts you tweeted by hand (e.g. via "x open"). Use "x publish" if you want
li-sync to tweet via the API instead. There is no scheduled state on X.

--id accepts the bare tweet ID or the full tweet URL (copy the link from X and
paste it as-is — the ID is extracted). Recording the ID is what enables
"x republish" later.`,
		Example: "  li-sync x mark cli-built-for-the-ai --id https://x.com/cprados/status/1234567890\n" +
			"  li-sync x mark cli-built-for-the-ai --at 2026-06-01 --id 1234567890",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runXMark(root, args[0], at, id)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "datetime (RFC3339 or YYYY-MM-DD[ HH:MM]) when it was tweeted (default: now)")
	cmd.Flags().StringVar(&id, "id", "", "tweet ID or full tweet URL (enables x republish)")
	return cmd
}

func newXUnmarkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unmark <slug>",
		Short: "Remove a post's entry from x-status.yaml",
		Long: `Remove a post's entry from x-status.yaml, reverting it to not-published.

Use before re-publishing a post whose tweet you deleted, so "x publish" won't
refuse with "already recorded".`,
		Example: "  li-sync x unmark cli-built-for-the-ai",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			return runXUnmark(root, args[0])
		},
	}
}
