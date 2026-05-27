// li-sync — track which blog posts have been queued or published on LinkedIn.
//
// The tool is a sidecar to the LinkedIn native post scheduler. It can either
// just audit the repo against a versioned state file (linkedin-status.yaml)
// and tell you which posts still need to be scheduled in LinkedIn's composer
// (manual mode), or publish/schedule posts directly via the LinkedIn API
// (after a one-time OAuth flow).
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"
)

const (
	stateFileName   = "linkedin-status.yaml"
	contentPostsDir = "content/posts"
	companionFile   = "linkedin-post.txt"
	composerURL     = "https://www.linkedin.com/feed/?shareActive=true"

	repoEnvVar = "LISYNC_REPO"
)

type post struct {
	Slug          string
	IndexPath     string
	Title         string
	Date          time.Time
	Draft         bool
	HasCompanion  bool
	CompanionPath string
}

type frontMatter struct {
	Title string    `yaml:"title" toml:"title"`
	Date  time.Time `yaml:"date" toml:"date"`
	Draft bool      `yaml:"draft" toml:"draft"`
}

type stateEntry struct {
	ScheduledFor time.Time `yaml:"scheduled_for"`
	Status       string    `yaml:"status"`
	Note         string    `yaml:"note,omitempty"`
}

type state struct {
	Posts map[string]stateEntry `yaml:"posts"`
}

func main() {
	repoFlag, cmd, args, err := parseGlobalArgs(os.Args[1:])
	if err != nil {
		die(err)
	}
	if cmd == "" {
		printUsage(os.Stderr)
		os.Exit(2)
	}

	switch cmd {
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return
	case "auth":
		// auth does not touch the Hugo repo
		if err := runAuth(args); err != nil {
			die(err)
		}
		return
	}

	root, err := resolveRepoRoot(repoFlag)
	if err != nil {
		die(err)
	}

	switch cmd {
	case "status":
		err = runStatus(root, args)
	case "mark":
		err = runMark(root, args)
	case "unmark":
		err = runUnmark(root, args)
	case "open":
		err = runOpen(root, args)
	case "publish":
		err = runPublish(root, args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage(os.Stderr)
		os.Exit(2)
	}

	if err != nil {
		die(err)
	}
}

// parseGlobalArgs strips an optional leading --repo <path> (or --repo=<path>)
// flag from os.Args before the subcommand. Returns the resolved repo flag
// value (may be ""), the command, and the remaining args for that command.
func parseGlobalArgs(argv []string) (repo, cmd string, rest []string, err error) {
	i := 0
	for i < len(argv) {
		a := argv[i]
		switch {
		case a == "--repo":
			if i+1 >= len(argv) {
				return "", "", nil, errors.New("--repo requires a path argument")
			}
			repo = argv[i+1]
			i += 2
		case strings.HasPrefix(a, "--repo="):
			repo = strings.TrimPrefix(a, "--repo=")
			if repo == "" {
				return "", "", nil, errors.New("--repo requires a path argument")
			}
			i++
		default:
			return repo, a, argv[i+1:], nil
		}
	}
	return repo, "", nil, nil
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, `li-sync — audit which blog posts are queued/published on LinkedIn

Usage:
  li-sync [--repo <path>] <command> [args]

Global flags:
  --repo <path>                           Path to the Hugo site root (the dir
                                          containing content/posts/). Overrides
                                          LISYNC_REPO and cwd auto-discovery.

Commands:
  status                                  List posts and their LinkedIn state
  mark <slug> --at <RFC3339>              Mark post as scheduled for that datetime
  mark <slug> --published [--at <date>]   Mark post as already published
  mark <slug> --note "text"               Attach a note to an existing entry
  unmark <slug>                           Remove the entry (revert to unscheduled)
  open <slug>                             Open companion in $EDITOR + LinkedIn composer in browser
  auth [--client-id ID --client-secret SECRET]
                                          One-time OAuth flow: authorize the app to post on your behalf.
                                          Credentials resolved from flags (saved), env vars, app.json, or prompt
  publish <slug> [--at] [--force] [--dry-run]
                                          Publish (or schedule) the companion to LinkedIn via API
  help                                    Show this message

Repo discovery precedence: --repo flag > LISYNC_REPO env > walk up from cwd
until a directory containing content/posts/ is found.

State is stored in linkedin-status.yaml at the repo root and is versioned in git.
OAuth tokens are stored locally outside the repo (XDG_CONFIG_HOME/li-sync/).
Base URL for article previews defaults to https://carlos.enredando.me and can be
overridden via LISYNC_BASE_URL.`)
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}

// ---------- repo discovery ----------

// resolveRepoRoot picks the Hugo site root following the precedence:
// explicit flag, LISYNC_REPO env var, or walk up from cwd looking for
// content/posts/.
func resolveRepoRoot(flagValue string) (string, error) {
	candidates := []string{flagValue, os.Getenv(repoEnvVar)}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			return "", fmt.Errorf("resolve %q: %w", c, err)
		}
		if !isDir(filepath.Join(abs, contentPostsDir)) {
			return "", fmt.Errorf("%s does not contain %s/", abs, contentPostsDir)
		}
		return abs, nil
	}
	return findRepoRoot()
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isDir(filepath.Join(dir, contentPostsDir)) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find Hugo repo root (no %s/ in any ancestor). Pass --repo <path> or set %s", contentPostsDir, repoEnvVar)
		}
		dir = parent
	}
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// ---------- posts ----------

func scanPosts(root string) ([]post, error) {
	postsDir := filepath.Join(root, contentPostsDir)
	entries, err := os.ReadDir(postsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", postsDir, err)
	}

	var posts []post
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := e.Name()
		indexPath := filepath.Join(postsDir, slug, "index.md")
		if _, err := os.Stat(indexPath); err != nil {
			continue
		}
		fm, err := parseFrontMatter(indexPath)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", indexPath, err)
		}
		companionPath := filepath.Join(postsDir, slug, companionFile)
		_, companionErr := os.Stat(companionPath)
		posts = append(posts, post{
			Slug:          slug,
			IndexPath:     indexPath,
			Title:         fm.Title,
			Date:          fm.Date,
			Draft:         fm.Draft,
			HasCompanion:  companionErr == nil,
			CompanionPath: companionPath,
		})
	}

	sort.Slice(posts, func(i, j int) bool {
		return posts[i].Date.Before(posts[j].Date)
	})
	return posts, nil
}

func parseFrontMatter(path string) (frontMatter, error) {
	var fm frontMatter
	f, err := os.Open(path)
	if err != nil {
		return fm, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	if !scanner.Scan() {
		return fm, errors.New("empty file")
	}
	delimiter := strings.TrimSpace(scanner.Text())
	var format string
	switch delimiter {
	case "---":
		format = "yaml"
	case "+++":
		format = "toml"
	default:
		return fm, fmt.Errorf("unrecognized front matter delimiter %q (expected --- or +++)", delimiter)
	}

	var body strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == delimiter {
			switch format {
			case "yaml":
				if err := yaml.Unmarshal([]byte(body.String()), &fm); err != nil {
					return fm, fmt.Errorf("parse YAML: %w", err)
				}
			case "toml":
				if _, err := toml.Decode(body.String(), &fm); err != nil {
					return fm, fmt.Errorf("parse TOML: %w", err)
				}
			}
			return fm, nil
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return fm, err
	}
	return fm, fmt.Errorf("unterminated %s front matter", format)
}

// ---------- state ----------

func statePath(root string) string {
	return filepath.Join(root, stateFileName)
}

func loadState(root string) (state, error) {
	var s state
	s.Posts = map[string]stateEntry{}

	data, err := os.ReadFile(statePath(root))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := yaml.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse %s: %w", stateFileName, err)
	}
	if s.Posts == nil {
		s.Posts = map[string]stateEntry{}
	}
	return s, nil
}

func saveState(root string, s state) error {
	if s.Posts == nil {
		s.Posts = map[string]stateEntry{}
	}
	var buf strings.Builder
	buf.WriteString("# linkedin-status.yaml — tracks which posts are queued/published on LinkedIn.\n")
	buf.WriteString("# Managed by li-sync (https://github.com/carlosprados/li-sync). Edit by hand only if you know what you're doing.\n\n")

	enc := yaml.NewEncoder(&writeAdapter{&buf})
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return err
	}
	enc.Close()
	return os.WriteFile(statePath(root), []byte(buf.String()), 0o644)
}

type writeAdapter struct{ b *strings.Builder }

func (w *writeAdapter) Write(p []byte) (int, error) { return w.b.Write(p) }

// ---------- status ----------

type rowStatus int

const (
	statusFuture rowStatus = iota
	statusDraft
	statusNoCompanion
	statusMissing
	statusScheduled
	statusPublished
)

func (s rowStatus) label() string {
	switch s {
	case statusFuture:
		return "future"
	case statusDraft:
		return "draft"
	case statusNoCompanion:
		return "no companion"
	case statusMissing:
		return "MISSING"
	case statusScheduled:
		return "scheduled"
	case statusPublished:
		return "published"
	}
	return "?"
}

type row struct {
	Slug      string
	PostDate  time.Time
	Status    rowStatus
	StateInfo string // e.g. "scheduled_for 2026-05-20" or note
	Action    string
}

func classify(p post, now time.Time, s state) row {
	r := row{Slug: p.Slug, PostDate: p.Date}

	if entry, ok := s.Posts[p.Slug]; ok {
		switch entry.Status {
		case "published":
			r.Status = statusPublished
			r.StateInfo = formatDate(entry.ScheduledFor)
		case "scheduled":
			r.Status = statusScheduled
			info := "scheduled_for " + formatDateTime(entry.ScheduledFor)
			if !entry.ScheduledFor.IsZero() && entry.ScheduledFor.Before(now) {
				info += " (past — promote to published?)"
			}
			r.StateInfo = info
		default:
			r.Status = statusScheduled
			r.StateInfo = "unknown status: " + entry.Status
		}
		if entry.Note != "" {
			r.StateInfo += "  // " + entry.Note
		}
		return r
	}

	if p.Draft {
		r.Status = statusDraft
		return r
	}
	if !p.HasCompanion {
		r.Status = statusNoCompanion
		r.Action = "missing linkedin-post.txt"
		return r
	}
	if p.Date.After(now) {
		r.Status = statusFuture
		return r
	}
	r.Status = statusMissing
	r.Action = "schedule it in LinkedIn"
	return r
}

func runStatus(root string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	all := fs.Bool("all", false, "show all rows (default hides future, draft, no-companion)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	posts, err := scanPosts(root)
	if err != nil {
		return err
	}
	s, err := loadState(root)
	if err != nil {
		return err
	}

	now := time.Now()
	rows := make([]row, 0, len(posts))
	pending := 0
	hidden := 0
	for _, p := range posts {
		r := classify(p, now, s)
		hideByDefault := r.Status == statusFuture || r.Status == statusDraft || r.Status == statusNoCompanion
		if !*all && hideByDefault {
			hidden++
			continue
		}
		if r.Status == statusMissing {
			pending++
		}
		rows = append(rows, r)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tPOST DATE\tLINKEDIN STATE\tACTION")
	for _, r := range rows {
		stateCol := r.Status.label()
		if r.StateInfo != "" {
			stateCol += "  " + r.StateInfo
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			r.Slug,
			formatDate(r.PostDate),
			stateCol,
			r.Action,
		)
	}
	w.Flush()

	fmt.Fprintln(os.Stdout)
	if pending == 0 {
		fmt.Fprintln(os.Stdout, "All caught up. No posts pending LinkedIn scheduling.")
	} else {
		fmt.Fprintf(os.Stdout, "%d post(s) pending LinkedIn scheduling.\n", pending)
	}
	if hidden > 0 && !*all {
		fmt.Fprintf(os.Stdout, "(%d row(s) hidden — future, draft, or no-companion. Use --all to see them.)\n", hidden)
	}
	return nil
}

// ---------- mark / unmark ----------

func runMark(root string, args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: mark <slug> [--at <datetime>] [--published] [--note text]")
	}
	slug := args[0]

	fs := flag.NewFlagSet("mark", flag.ContinueOnError)
	atFlag := fs.String("at", "", "datetime (RFC3339 or YYYY-MM-DD) when the post was/will be published on LinkedIn")
	publishedFlag := fs.Bool("published", false, "mark as already published (default is scheduled)")
	noteFlag := fs.String("note", "", "optional free-form note")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments after slug: %v", fs.Args())
	}

	posts, err := scanPosts(root)
	if err != nil {
		return err
	}
	if !slugExists(posts, slug) {
		return fmt.Errorf("no post directory named %q under %s/", slug, contentPostsDir)
	}

	s, err := loadState(root)
	if err != nil {
		return err
	}

	entry := s.Posts[slug]
	if *atFlag != "" {
		t, err := parseFlexibleTime(*atFlag)
		if err != nil {
			return fmt.Errorf("--at: %w", err)
		}
		entry.ScheduledFor = t
	}
	if *publishedFlag {
		entry.Status = "published"
	} else if entry.Status == "" {
		entry.Status = "scheduled"
	}
	if *noteFlag != "" {
		entry.Note = *noteFlag
	}
	if entry.Status == "scheduled" && entry.ScheduledFor.IsZero() {
		return errors.New("scheduled entries need --at <datetime>")
	}

	s.Posts[slug] = entry
	if err := saveState(root, s); err != nil {
		return err
	}

	fmt.Printf("marked %s as %s", slug, entry.Status)
	if !entry.ScheduledFor.IsZero() {
		fmt.Printf(" for %s", formatDateTime(entry.ScheduledFor))
	}
	fmt.Println()
	return nil
}

func runUnmark(root string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: unmark <slug>")
	}
	slug := args[0]
	s, err := loadState(root)
	if err != nil {
		return err
	}
	if _, ok := s.Posts[slug]; !ok {
		return fmt.Errorf("no entry for %q in %s", slug, stateFileName)
	}
	delete(s.Posts, slug)
	if err := saveState(root, s); err != nil {
		return err
	}
	fmt.Printf("removed %s from %s\n", slug, stateFileName)
	return nil
}

// ---------- open ----------

func runOpen(root string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: open <slug>")
	}
	slug := args[0]
	companion := filepath.Join(root, contentPostsDir, slug, companionFile)
	if _, err := os.Stat(companion); err != nil {
		return fmt.Errorf("no %s for %q", companionFile, slug)
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nvim"
	}
	fmt.Fprintf(os.Stderr, "opening %s in %s\n", companion, editor)
	cmd := exec.Command(editor, companion)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor exited with error: %w", err)
	}

	fmt.Fprintf(os.Stderr, "opening LinkedIn composer in browser: %s\n", composerURL)
	if err := openURL(composerURL); err != nil {
		fmt.Fprintf(os.Stderr, "could not open browser automatically: %v\nopen this URL manually: %s\n", err, composerURL)
	}
	fmt.Fprintln(os.Stderr, "after scheduling on LinkedIn, run: li-sync mark "+slug+" --at <YYYY-MM-DDTHH:MM:SS+02:00>")
	return nil
}

func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	return cmd.Start()
}

// ---------- helpers ----------

func slugExists(posts []post, slug string) bool {
	for _, p := range posts {
		if p.Slug == slug {
			return true
		}
	}
	return false
}

func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05", s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime %q (use RFC3339 or YYYY-MM-DD[ HH:MM])", s)
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02")
}

func formatDateTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04 MST")
}
