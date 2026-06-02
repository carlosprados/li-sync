package main

import (
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
			return runEdit(root, args[0])
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
			return runRepublish(root, args[0], at, noVerify)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "override publish/schedule datetime for the new post (default: post's front-matter date)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article/og:image preflight (not recommended)")
	return cmd
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
			return runPublish(root, args[0], at, force, dryRun, noVerify)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "override publish/schedule datetime (default: post's front-matter date)")
	cmd.Flags().BoolVar(&force, "force", false, "publish even if the slug already has a state entry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "run preflight + print payload, no API call (no auth needed)")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip the article/og:image preflight (not recommended)")
	return cmd
}
