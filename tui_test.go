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
	if !strings.Contains(view, "[q]uit") {
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

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// The confirm state machine must gate every write action and never launch one
// without an explicit "y".
func TestUIConfirmFlow(t *testing.T) {
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "past-missing", date: "2026-01-01"},
	})
	m, err := newUIModel(root, "https://example.com")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// "p" arms a publish confirmation — but does NOT run anything yet.
	armed, _ := sized.Update(key('p'))
	am := armed.(uiModel)
	if am.mode != modeConfirm {
		t.Fatalf("after 'p', mode = %v, want modeConfirm", am.mode)
	}
	if am.pending.kind != actionPublish || am.pending.slug != "past-missing" {
		t.Errorf("pending = %+v, want publish/past-missing", am.pending)
	}
	if !strings.Contains(am.View(), "confirm") {
		t.Errorf("confirm view should prompt for confirmation:\n%s", am.View())
	}

	// "n" cancels back to browsing with no action taken.
	cancelled, _ := am.Update(key('n'))
	cm := cancelled.(uiModel)
	if cm.mode != modeBrowse {
		t.Errorf("after 'n', mode = %v, want modeBrowse", cm.mode)
	}

	// "d" (dry-run) goes straight to running and returns work to do.
	running, cmd := cm.Update(key('d'))
	rm := running.(uiModel)
	if rm.mode != modeRunning {
		t.Errorf("after 'd', mode = %v, want modeRunning", rm.mode)
	}
	if cmd == nil {
		t.Error("dry-run should return a command to execute the op")
	}
}
