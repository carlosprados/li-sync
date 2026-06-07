package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	xStateFileName = "x-status.yaml"
	xCompanionFile = "x-post.txt"

	// tweetMaxWeightedLen is X's limit for standard accounts, counted in
	// weighted characters (URLs count as 23 regardless of length).
	tweetMaxWeightedLen = 280

	// tcoURLLen is the fixed weight X assigns to any URL (t.co wrapping).
	tcoURLLen = 23
)

func xCompanionPath(root, slug string) string {
	return filepath.Join(root, contentPostsDir, slug, xCompanionFile)
}

func hasXCompanion(root, slug string) bool {
	_, err := os.Stat(xCompanionPath(root, slug))
	return err == nil
}

func loadXState(root string) (state, error) {
	return loadStateFrom(root, xStateFileName)
}

func saveXState(root string, s state) error {
	header := "# x-status.yaml — tracks which posts have been published on X (Twitter).\n" +
		"# Managed by li-sync (https://github.com/carlosprados/li-sync). Edit by hand only if you know what you're doing.\n\n"
	return saveStateTo(root, xStateFileName, header, s)
}

// tweetLen approximates X's weighted character count: every http(s) URL token
// counts as tcoURLLen (t.co wraps all links), everything else counts one per
// rune. It deliberately skips X's full weighting rules (CJK/emoji weights,
// URL detection inside punctuation) — the API is the authority and rejects
// over-limit tweets; this pre-check just catches the obvious cases early.
func tweetLen(s string) int {
	n := 0
	for _, field := range strings.Fields(s) {
		if isWeightedURL(field) {
			n += tcoURLLen
		} else {
			n += len([]rune(field))
		}
	}
	// Fields() drops the separators; count every whitespace rune as 1.
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			n++
		}
	}
	return n
}

func isWeightedURL(token string) bool {
	if !strings.HasPrefix(token, "http://") && !strings.HasPrefix(token, "https://") {
		return false
	}
	u, err := url.Parse(token)
	return err == nil && u.Host != ""
}

// classifyX maps a post to its X state. Mirrors classify but with the X
// companion and no "scheduled" state — X cannot schedule via the API, so an
// entry is either published or absent.
func classifyX(p post, now time.Time, s state) row {
	r := row{Slug: p.Slug, PostDate: p.Date}

	if entry, ok := s.Posts[p.Slug]; ok {
		switch entry.Status {
		case "published":
			r.Status = statusPublished
			r.StateInfo = formatDate(entry.ScheduledFor)
		default:
			r.Status = statusPublished
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
		r.Action = "missing " + xCompanionFile
		return r
	}
	if p.Date.After(now) {
		r.Status = statusFuture
		r.Action = "publish on/after " + formatDate(p.Date)
		return r
	}
	r.Status = statusMissing
	r.Action = "li-sync x publish " + p.Slug
	return r
}

// buildXStatusReport is the X counterpart of buildStatusReport. scanPosts
// reports the LinkedIn companion, so HasCompanion is re-derived from
// x-post.txt here, leaving the post model untouched.
func buildXStatusReport(root string, all bool, now time.Time) (StatusReport, error) {
	posts, err := scanPosts(root)
	if err != nil {
		return StatusReport{}, err
	}
	s, err := loadXState(root)
	if err != nil {
		return StatusReport{}, err
	}

	rep := StatusReport{Rows: make([]row, 0, len(posts))}
	for _, p := range posts {
		p.HasCompanion = hasXCompanion(root, p.Slug)
		p.CompanionPath = xCompanionPath(root, p.Slug)
		r := classifyX(p, now, s)
		hideByDefault := r.Status == statusFuture || r.Status == statusDraft || r.Status == statusNoCompanion
		if !all && hideByDefault {
			rep.Hidden++
			continue
		}
		if r.Status == statusMissing {
			rep.Pending++
		}
		rep.Rows = append(rep.Rows, r)
	}
	return rep, nil
}

func runXStatus(root string, all bool) error {
	report, err := buildXStatusReport(root, all, time.Now())
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tPOST DATE\tX STATE\tACTION")
	for _, r := range report.Rows {
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
	if report.Pending == 0 {
		fmt.Fprintln(os.Stdout, "All caught up. No posts pending on X.")
	} else {
		fmt.Fprintf(os.Stdout, "%d post(s) pending on X.\n", report.Pending)
	}
	if report.Hidden > 0 && !all {
		fmt.Fprintf(os.Stdout, "(%d row(s) hidden — future, draft, or no-companion. Use --all to see them.)\n", report.Hidden)
	}
	return nil
}

// ---------- x open ----------

// xIntentURL is X's web intent composer. Unlike LinkedIn's composer it accepts
// the tweet text as a query param, so the manual path needs no copy/paste.
const xIntentURL = "https://x.com/intent/post"

func buildXIntentURL(text string) string {
	v := url.Values{}
	v.Set("text", text)
	return xIntentURL + "?" + v.Encode()
}

// runXOpen is the manual, no-API publishing path for X: opens the browser on
// X's web intent with the tweet text from x-post.txt already filled in. After
// posting, record it with `x mark`.
func runXOpen(root, slug string) error {
	companion := xCompanionPath(root, slug)
	body, err := os.ReadFile(companion)
	if err != nil {
		return fmt.Errorf("no %s for %q", xCompanionFile, slug)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("%s is empty", companion)
	}
	if n := tweetLen(text); n > tweetMaxWeightedLen {
		fmt.Fprintf(os.Stderr, "warning: companion is ~%d weighted chars, over X's %d limit — the composer will refuse it as-is\n", n, tweetMaxWeightedLen)
	}

	intent := buildXIntentURL(text)
	fmt.Fprintln(os.Stderr, "opening X composer in browser with the tweet pre-filled...")
	if err := openURL(intent); err != nil {
		fmt.Fprintf(os.Stderr, "could not open browser automatically: %v\nopen this URL manually: %s\n", err, intent)
	}
	fmt.Fprintln(os.Stderr, "after posting on X, run: li-sync x mark "+slug+" --id <tweet URL or ID>")
	return nil
}

// ---------- x mark / unmark ----------

// parseTweetID extracts the numeric tweet ID from either a bare ID or a full
// tweet URL (https://x.com/<user>/status/<id>[?query]). The ID is what
// `x republish` needs to delete the old tweet, so garbage is rejected here
// rather than discovered at republish time.
func parseTweetID(s string) (string, error) {
	id := strings.TrimSpace(s)
	if _, rest, found := strings.Cut(id, "/status/"); found {
		id = rest
		if i := strings.IndexAny(id, "?/"); i >= 0 {
			id = id[:i]
		}
	}
	if id == "" {
		return "", fmt.Errorf("no tweet ID in %q", s)
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%q is not a tweet ID or tweet URL (expected a number or .../status/<number>)", s)
		}
	}
	return id, nil
}

// runXMark records a post as published on X (trust-based, no API call).
// at optionally backdates the entry; id is the tweet ID (or full tweet URL),
// stored in the entry's note field.
func runXMark(root, slug, at, id string) error {
	posts, err := scanPosts(root)
	if err != nil {
		return err
	}
	if !slugExists(posts, slug) {
		return fmt.Errorf("no post directory named %q under %s/", slug, contentPostsDir)
	}

	s, err := loadXState(root)
	if err != nil {
		return err
	}

	entry := s.Posts[slug]
	entry.Status = "published"
	entry.ScheduledFor = time.Now()
	if at != "" {
		t, err := parseFlexibleTime(at)
		if err != nil {
			return fmt.Errorf("--at: %w", err)
		}
		entry.ScheduledFor = t
	}
	if id != "" {
		tweetID, err := parseTweetID(id)
		if err != nil {
			return fmt.Errorf("--id: %w", err)
		}
		entry.Note = tweetID
	}

	s.Posts[slug] = entry
	if err := saveXState(root, s); err != nil {
		return err
	}

	fmt.Printf("marked %s as published on X", slug)
	if entry.Note != "" {
		fmt.Printf(" (tweet ID: %s)", entry.Note)
	}
	fmt.Println()
	return nil
}

func runXUnmark(root, slug string) error {
	s, err := loadXState(root)
	if err != nil {
		return err
	}
	if _, ok := s.Posts[slug]; !ok {
		return fmt.Errorf("no entry for %q in %s", slug, xStateFileName)
	}
	delete(s.Posts, slug)
	if err := saveXState(root, s); err != nil {
		return err
	}
	fmt.Printf("removed %s from %s\n", slug, xStateFileName)
	return nil
}
