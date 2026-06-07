package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXStateRoundTrip(t *testing.T) {
	root := t.TempDir()

	s, err := loadXState(root)
	if err != nil {
		t.Fatalf("loadXState (missing file): %v", err)
	}
	if s.Posts == nil {
		t.Fatal("Posts map is nil after loading a missing state file")
	}

	s.Posts["agentic-ai-ch04-reflection"] = stateEntry{
		Status:       "published",
		ScheduledFor: mustTime(t, "2026-06-02T13:54:20+02:00"),
		Note:         "1234567890123456789",
	}
	if err := saveXState(root, s); err != nil {
		t.Fatalf("saveXState: %v", err)
	}

	// The X store is its own file, and the LinkedIn one is untouched.
	if _, err := os.Stat(filepath.Join(root, xStateFileName)); err != nil {
		t.Fatalf("x state file not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, stateFileName)); err == nil {
		t.Fatal("saveXState wrote the LinkedIn state file")
	}

	got, err := loadXState(root)
	if err != nil {
		t.Fatalf("loadXState (round trip): %v", err)
	}
	entry, ok := got.Posts["agentic-ai-ch04-reflection"]
	if !ok {
		t.Fatal("entry missing after round trip")
	}
	if entry.Status != "published" || entry.Note != "1234567890123456789" {
		t.Errorf("entry = %+v, want published with the tweet ID note", entry)
	}
}

func TestParseTweetID(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"bare id", "1931845207894528123", "1931845207894528123", false},
		{"full url", "https://x.com/cprados/status/1931845207894528123", "1931845207894528123", false},
		{"url with query", "https://x.com/cprados/status/1931845207894528123?s=20&t=abc", "1931845207894528123", false},
		{"url with trailing path", "https://x.com/cprados/status/1931845207894528123/photo/1", "1931845207894528123", false},
		{"padded", "  1931845207894528123 ", "1931845207894528123", false},
		{"garbage", "publicado desde el móvil", "", true},
		{"url without id", "https://x.com/cprados/status/", "", true},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTweetID(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseTweetID(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseTweetID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestClassifyX(t *testing.T) {
	now := mustTime(t, "2026-06-02T12:00:00+02:00")

	tests := []struct {
		name       string
		p          post
		state      state
		wantStatus rowStatus
		infoSub    string
		actionSub  string
	}{
		{
			name:       "recorded published",
			p:          post{Slug: "a", HasCompanion: true, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{"a": {Status: "published", ScheduledFor: mustTime(t, "2026-01-02"), Note: "111"}}},
			wantStatus: statusPublished,
			infoSub:    "111",
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
			actionSub:  xCompanionFile,
		},
		{
			name:       "future is not schedulable",
			p:          post{Slug: "f", HasCompanion: true, Date: mustTime(t, "2026-12-01")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusFuture,
			actionSub:  "publish on/after",
		},
		{
			name:       "past with companion is missing",
			p:          post{Slug: "g", HasCompanion: true, Date: mustTime(t, "2026-01-01")},
			state:      state{Posts: map[string]stateEntry{}},
			wantStatus: statusMissing,
			actionSub:  "x publish g",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := classifyX(tt.p, now, tt.state)
			if r.Status != tt.wantStatus {
				t.Errorf("Status = %s, want %s", r.Status.label(), tt.wantStatus.label())
			}
			if r.Status == statusScheduled {
				t.Error("classifyX must never produce a scheduled state")
			}
			if tt.infoSub != "" && !strings.Contains(r.StateInfo, tt.infoSub) {
				t.Errorf("StateInfo = %q, want substring %q", r.StateInfo, tt.infoSub)
			}
			if tt.actionSub != "" && !strings.Contains(r.Action, tt.actionSub) {
				t.Errorf("Action = %q, want substring %q", r.Action, tt.actionSub)
			}
		})
	}
}
