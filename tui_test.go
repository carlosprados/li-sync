package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	m, err := newUIModel(root, "https://example.com", "")
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

	m, err := newUIModel(root, "https://example.com", "")
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

// A long companion (lines that wrap many times) must not blow the view past the
// terminal height — that's the bug that pushed the table header off-screen.
func TestUIViewFixedHeight(t *testing.T) {
	root := t.TempDir()
	mk := func(slug, companion string) {
		dir := filepath.Join(root, "content", "posts", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "index.md"),
			"---\ntitle: \""+slug+"\"\ndate: 2026-01-01\ndraft: false\n---\nbody\n")
		writeFile(t, filepath.Join(dir, "linkedin-post.txt"), companion)
	}
	long := strings.Repeat("a very long line that wraps across the terminal several times over and over. ", 30)
	mk("a-short", "tiny companion")
	mk("b-long", long+"\n"+long+"\n"+long+"\n"+long)

	const H = 24
	m, err := newUIModel(root, "https://example.com", "")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: H})

	if got := lipgloss.Height(model.View()); got > H {
		t.Errorf("view height %d exceeds terminal height %d", got, H)
	}
	// Moving the selection (onto the long companion) must not change that.
	moved, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := lipgloss.Height(moved.View()); got > H {
		t.Errorf("view height after moving = %d, exceeds terminal height %d", got, H)
	}
}

// With no repo, the TUI opens the directory picker instead of failing.
func TestUIRepoPickerStart(t *testing.T) {
	m, err := newUIModel("", "https://example.com", "")
	if err != nil {
		t.Fatalf("newUIModel(\"\"): %v", err)
	}
	if m.mode != modePickRepo {
		t.Fatalf("mode = %v, want modePickRepo when no repo is given", m.mode)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := sized.View()
	if !strings.Contains(view, "Hugo site root") {
		t.Errorf("picker view missing the prompt:\n%s", view)
	}
}

// A post with both companions exposes both platforms in the selector; toggling
// X off and confirming carries LinkedIn-only targets through to the action.
func TestUISelectPlatformDual(t *testing.T) {
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "dual-post", date: "2026-01-01"},
	})
	// Give it an X companion too — newTestRepo only writes linkedin-post.txt.
	writeFile(t, filepath.Join(root, "content", "posts", "dual-post", "x-post.txt"),
		"a tweet https://example.com/posts/dual-post/\n")

	m, err := newUIModel(root, "https://example.com", "")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// The dual dashboard shows both platform columns.
	if v := sized.View(); !strings.Contains(v, "LINKEDIN") || !strings.Contains(v, "x-post.txt present") {
		t.Errorf("dual view missing LINKEDIN column or X companion note:\n%s", v)
	}

	// "p" opens the selector with BOTH platforms available and pre-selected.
	picked, _ := sized.Update(key('p'))
	pm := picked.(uiModel)
	if pm.mode != modeSelectPlatform {
		t.Fatalf("after 'p', mode = %v, want modeSelectPlatform", pm.mode)
	}
	if !pm.availLI || !pm.availX || !pm.selLI || !pm.selX {
		t.Fatalf("selector availLI:%v availX:%v selLI:%v selX:%v, want all true",
			pm.availLI, pm.availX, pm.selLI, pm.selX)
	}

	// Toggle X off, then confirm — only LinkedIn should carry through.
	toggled, _ := pm.Update(key('x'))
	tm := toggled.(uiModel)
	if tm.selX {
		t.Errorf("after 'x' toggle, selX = true, want false")
	}
	armed, _ := tm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := armed.(uiModel)
	if am.mode != modeConfirm {
		t.Fatalf("after 'enter', mode = %v, want modeConfirm", am.mode)
	}
	if !am.pending.li || am.pending.x {
		t.Errorf("targets = LI:%v X:%v, want LI only", am.pending.li, am.pending.x)
	}
	if !strings.Contains(am.View(), "LinkedIn") {
		t.Errorf("confirm view should name the LinkedIn target:\n%s", am.View())
	}
}

// From the table, "c" reopens the repo picker without losing the model.
func TestUIChangeRepoKey(t *testing.T) {
	root := newTestRepo(t, []struct {
		slug        string
		date        string
		draft       bool
		noCompanion bool
	}{
		{slug: "past-missing", date: "2026-01-01"},
	})
	m, err := newUIModel(root, "https://example.com", "")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	changed, _ := sized.Update(key('c'))
	if changed.(uiModel).mode != modePickRepo {
		t.Errorf("after 'c', mode = %v, want modePickRepo", changed.(uiModel).mode)
	}
}

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
	m, err := newUIModel(root, "https://example.com", "")
	if err != nil {
		t.Fatalf("newUIModel: %v", err)
	}
	sized, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})

	// "p" opens the platform selector — it does NOT run or confirm anything yet.
	picked, _ := sized.Update(key('p'))
	pm := picked.(uiModel)
	if pm.mode != modeSelectPlatform {
		t.Fatalf("after 'p', mode = %v, want modeSelectPlatform", pm.mode)
	}
	if pm.pending.kind != actionPublish || pm.pending.slug != "past-missing" {
		t.Errorf("pending = %+v, want publish/past-missing", pm.pending)
	}
	// The test repo has only a LinkedIn companion, so X is unavailable and only
	// LinkedIn is pre-selected.
	if !pm.selLI || pm.selX {
		t.Errorf("selector pre-selection = LI:%v X:%v, want LI only", pm.selLI, pm.selX)
	}

	// "enter" with a platform selected arms the confirmation — still nothing run.
	armed, _ := pm.Update(tea.KeyMsg{Type: tea.KeyEnter})
	am := armed.(uiModel)
	if am.mode != modeConfirm {
		t.Fatalf("after 'enter', mode = %v, want modeConfirm", am.mode)
	}
	if !am.pending.li || am.pending.x {
		t.Errorf("confirmed targets = LI:%v X:%v, want LI only", am.pending.li, am.pending.x)
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

	// "d" (dry-run) opens the selector; "enter" then goes straight to running
	// (dry-run is harmless, no confirm step) and returns work to do.
	dpicked, _ := cm.Update(key('d'))
	if dpicked.(uiModel).mode != modeSelectPlatform {
		t.Fatalf("after 'd', mode = %v, want modeSelectPlatform", dpicked.(uiModel).mode)
	}
	running, cmd := dpicked.Update(tea.KeyMsg{Type: tea.KeyEnter})
	rm := running.(uiModel)
	if rm.mode != modeRunning {
		t.Errorf("after dry-run 'enter', mode = %v, want modeRunning", rm.mode)
	}
	if cmd == nil {
		t.Error("dry-run should return a command to execute the op")
	}
}
