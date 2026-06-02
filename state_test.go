package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	root := t.TempDir()

	// loadState on a repo with no state file yields an empty, usable map.
	s, err := loadState(root)
	if err != nil {
		t.Fatalf("loadState (missing file): %v", err)
	}
	if s.Posts == nil {
		t.Fatal("Posts map is nil after loading a missing state file")
	}
	if len(s.Posts) != 0 {
		t.Errorf("expected 0 posts, got %d", len(s.Posts))
	}

	s.Posts["agentic-ai-ch04-reflection"] = stateEntry{
		Status:       "published",
		ScheduledFor: mustTime(t, "2026-06-02T13:54:20+02:00"),
		Note:         "urn:li:share:7467544132807331840",
	}
	if err := saveState(root, s); err != nil {
		t.Fatalf("saveState: %v", err)
	}

	// The file is written at the repo root with the expected name.
	if _, err := os.Stat(filepath.Join(root, stateFileName)); err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	got, err := loadState(root)
	if err != nil {
		t.Fatalf("loadState (round trip): %v", err)
	}
	entry, ok := got.Posts["agentic-ai-ch04-reflection"]
	if !ok {
		t.Fatal("entry missing after round trip")
	}
	if entry.Status != "published" || entry.Note != "urn:li:share:7467544132807331840" {
		t.Errorf("entry = %+v, want published with the URN note", entry)
	}
	if !entry.ScheduledFor.Equal(mustTime(t, "2026-06-02T13:54:20+02:00")) {
		t.Errorf("ScheduledFor = %v, not preserved", entry.ScheduledFor)
	}
}

func TestResolveRepoRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, contentPostsDir), 0o755); err != nil {
		t.Fatal(err)
	}

	// Explicit value pointing at a valid Hugo root resolves to its absolute path.
	got, err := resolveRepoRoot(root)
	if err != nil {
		t.Fatalf("resolveRepoRoot(valid): %v", err)
	}
	abs, _ := filepath.Abs(root)
	if got != abs {
		t.Errorf("resolveRepoRoot = %q, want %q", got, abs)
	}

	// A directory without content/posts/ is rejected.
	notARepo := t.TempDir()
	if _, err := resolveRepoRoot(notARepo); err == nil {
		t.Errorf("expected error for %q (no %s/), got nil", notARepo, contentPostsDir)
	}
}
