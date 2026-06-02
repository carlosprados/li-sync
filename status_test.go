package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := parseFlexibleTime(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestClassify(t *testing.T) {
	now := mustTime(t, "2026-06-02T12:00:00+02:00")

	tests := []struct {
		name       string
		p          post
		state      state
		wantStatus rowStatus
		infoSub    string // substring expected in StateInfo ("" = skip)
		actionSub  string
	}{
		{
			name:       "recorded published",
			p:          post{Slug: "a", HasCompanion: true, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{"a": {Status: "published", ScheduledFor: mustTime(t, "2026-01-02")}}},
			wantStatus: statusPublished,
		},
		{
			name:       "recorded scheduled future",
			p:          post{Slug: "b", HasCompanion: true},
			state:      state{Posts: map[string]stateEntry{"b": {Status: "scheduled", ScheduledFor: mustTime(t, "2026-12-01T10:00:00+02:00")}}},
			wantStatus: statusScheduled,
			infoSub:    "scheduled_for",
		},
		{
			name:       "recorded scheduled in the past flags promote",
			p:          post{Slug: "c", HasCompanion: true},
			state:      state{Posts: map[string]stateEntry{"c": {Status: "scheduled", ScheduledFor: mustTime(t, "2026-01-01T10:00:00+02:00")}}},
			wantStatus: statusScheduled,
			infoSub:    "past — promote",
		},
		{
			name:       "draft",
			p:          post{Slug: "d", Draft: true, HasCompanion: true, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusDraft,
		},
		{
			name:       "no companion",
			p:          post{Slug: "e", HasCompanion: false, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusNoCompanion,
			actionSub:  "missing linkedin-post.txt",
		},
		{
			name:       "future post",
			p:          post{Slug: "f", HasCompanion: true, Date: mustTime(t, "2026-12-31")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusFuture,
		},
		{
			name:       "missing (past, has companion, not recorded)",
			p:          post{Slug: "g", HasCompanion: true, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusMissing,
			actionSub:  "schedule it in LinkedIn",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := classify(tc.p, now, tc.state)
			if r.Status != tc.wantStatus {
				t.Errorf("Status = %v (%s), want %v", r.Status, r.Status.label(), tc.wantStatus)
			}
			if tc.infoSub != "" && !strings.Contains(r.StateInfo, tc.infoSub) {
				t.Errorf("StateInfo = %q, want substring %q", r.StateInfo, tc.infoSub)
			}
			if tc.actionSub != "" && !strings.Contains(r.Action, tc.actionSub) {
				t.Errorf("Action = %q, want substring %q", r.Action, tc.actionSub)
			}
		})
	}
}

// newTestRepo builds a throwaway Hugo-like repo with the given posts and returns
// its root. Each post is content/posts/<slug>/index.md (+ a companion unless
// noCompanion).
func newTestRepo(t *testing.T, posts []struct {
	slug        string
	date        string
	draft       bool
	noCompanion bool
}) string {
	t.Helper()
	root := t.TempDir()
	for _, p := range posts {
		dir := filepath.Join(root, "content", "posts", p.slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		draft := "false"
		if p.draft {
			draft = "true"
		}
		writeFile(t, filepath.Join(dir, "index.md"),
			"---\ntitle: \""+p.slug+"\"\ndate: "+p.date+"\ndraft: "+draft+"\n---\n\nbody\n")
		if !p.noCompanion {
			writeFile(t, filepath.Join(dir, "linkedin-post.txt"), "companion body\n")
		}
	}
	return root
}

func TestBuildStatusReport(t *testing.T) {
	now := mustTime(t, "2026-06-02T12:00:00+02:00")
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "past-missing", date: "2026-01-01"},               // MISSING (counts as pending)
		{slug: "future-post", date: "2026-12-01"},                // future → hidden by default
		{slug: "a-draft", date: "2026-01-01", draft: true},       // draft → hidden by default
		{slug: "no-comp", date: "2026-01-01", noCompanion: true}, // no companion → hidden by default
	})

	// Default view hides future/draft/no-companion.
	rep, err := buildStatusReport(root, false, now)
	if err != nil {
		t.Fatalf("buildStatusReport: %v", err)
	}
	if len(rep.Rows) != 1 {
		t.Errorf("default Rows = %d, want 1", len(rep.Rows))
	}
	if rep.Pending != 1 {
		t.Errorf("Pending = %d, want 1", rep.Pending)
	}
	if rep.Hidden != 3 {
		t.Errorf("Hidden = %d, want 3", rep.Hidden)
	}

	// --all shows every row and hides nothing.
	repAll, err := buildStatusReport(root, true, now)
	if err != nil {
		t.Fatalf("buildStatusReport(all): %v", err)
	}
	if len(repAll.Rows) != 4 {
		t.Errorf("all Rows = %d, want 4", len(repAll.Rows))
	}
	if repAll.Hidden != 0 {
		t.Errorf("all Hidden = %d, want 0", repAll.Hidden)
	}
}
