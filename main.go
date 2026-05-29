// li-sync — track which blog posts have been queued or published on LinkedIn.
//
// The tool is a sidecar to the LinkedIn native post scheduler. It can either
// just audit the repo against a versioned state file (linkedin-status.yaml)
// and tell you which posts still need to be scheduled in LinkedIn's composer
// (manual mode), or publish/schedule posts directly via the LinkedIn API
// (after a one-time OAuth flow).
//
// The CLI is built with Cobra (commands/flags + auto-generated help) and Viper
// (config file + env binding). See root.go for wiring.
package main

import (
	"bufio"
	"errors"
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
	Slug          string // directory name — the identifier used in commands and the state file
	URLSlug       string // front-matter slug if set, else Slug — what Hugo publishes the page under
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
	Slug  string    `yaml:"slug" toml:"slug"`
}

type stateEntry struct {
	ScheduledFor time.Time `yaml:"scheduled_for"`
	Status       string    `yaml:"status"`
	Note         string    `yaml:"note,omitempty"`
}

type state struct {
	Posts map[string]stateEntry `yaml:"posts"`
}

// ---------- repo discovery ----------

// resolveRepoRoot picks the Hugo site root following the precedence:
// explicit value (Viper: --repo flag > LISYNC_REPO env > config file), or
// walk up from cwd looking for content/posts/.
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
		urlSlug := fm.Slug // Hugo publishes under the front-matter slug when set...
		if urlSlug == "" {
			urlSlug = slug // ...otherwise the directory name is the URL segment.
		}
		posts = append(posts, post{
			Slug:          slug,
			URLSlug:       urlSlug,
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

func runStatus(root string, all bool) error {
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
		if !all && hideByDefault {
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
	if hidden > 0 && !all {
		fmt.Fprintf(os.Stdout, "(%d row(s) hidden — future, draft, or no-companion. Use --all to see them.)\n", hidden)
	}
	return nil
}

// ---------- mark / unmark ----------

func runMark(root, slug, at string, published bool, note string) error {
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
	if at != "" {
		t, err := parseFlexibleTime(at)
		if err != nil {
			return fmt.Errorf("--at: %w", err)
		}
		entry.ScheduledFor = t
	}
	if published {
		entry.Status = "published"
	} else if entry.Status == "" {
		entry.Status = "scheduled"
	}
	if note != "" {
		entry.Note = note
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

func runUnmark(root, slug string) error {
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

func runOpen(root, slug string) error {
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
