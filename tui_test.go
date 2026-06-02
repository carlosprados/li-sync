package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The Bubble Tea program needs a TTY, but the model itself (build + View) is
// pure string work, so we can exercise it headlessly: construct it against a
// throwaway repo, feed a window size, and assert the rendered frame.
func TestUIModelRender(t *testing.T) {
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "past-missing", date: "2026-01-01"},
		{slug: "future-post", date: "2026-12-01"},
	})

	m, err := newUIModel(root, "https://example.com")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}

	// Default (actionable) view: the past post shows, the future one is hidden.
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view := updated.View()

	if !strings.Contains(view, "past-missing") {
		t.Errorf("view missing the actionable post row:\n%s", view)
	}
	if strings.Contains(view, "future-post") {
		t.Errorf("future post should be hidden in the default view:\n%s", view)
	}
	if !strings.Contains(view, "li-sync") {
		t.Errorf("view missing the title bar:\n%s", view)
	}
	if !strings.Contains(view, "[q] quit") {
		t.Errorf("view missing the footer keys:\n%s", view)
	}
}

func TestUIModelToggleAll(t *testing.T) {
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "past-missing", date: "2026-01-01"},
		{slug: "future-post", date: "2026-12-01"},
	})

	m, err := newUIModel(root, "https://example.com")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// Pressing "a" flips to the all view, which must surface the future post.
	toggled, _ := sized.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	view := toggled.View()
	if !strings.Contains(view, "future-post") {
		t.Errorf("toggling all should reveal the future post:\n%s", view)
	}
}
