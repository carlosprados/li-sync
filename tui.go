package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// This is the additive, interactive front-end. Browsing is read-only; write
// actions (publish, edit, republish, unmark) are also available here but call
// the SAME core functions the CLI uses, behind an explicit confirmation. The CLI
// subcommands remain the primary, fully scriptable path — the TUI never becomes
// the only way to do anything, and it requires a TTY.

var (
	uiTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1)
	uiFooterStyle = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	uiPreviewBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	uiErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	uiOkStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	uiWarnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	uiTitleBold   = lipgloss.NewStyle().Bold(true)
)

func stateColor(s rowStatus) lipgloss.Color {
	switch s {
	case statusMissing:
		return lipgloss.Color("196") // red — needs action
	case statusPublished:
		return lipgloss.Color("42") // green
	case statusScheduled:
		return lipgloss.Color("214") // amber
	case statusFuture:
		return lipgloss.Color("63") // blue
	default:
		return lipgloss.Color("245") // grey — draft / no companion
	}
}

// uiMode is the TUI state machine: browse the table, confirm a pending action,
// watch it run, then read the result.
type uiMode int

const (
	modeBrowse uiMode = iota
	modeConfirm
	modeRunning
	modeResult
	modePickRepo
)

type actionKind int

const (
	actionDryRun actionKind = iota
	actionPublish
	actionEdit
	actionRepublish
	actionUnmark
)

func (a actionKind) verb() string {
	switch a {
	case actionDryRun:
		return "Dry-run"
	case actionPublish:
		return "Publish"
	case actionEdit:
		return "Edit commentary"
	case actionRepublish:
		return "Republish (delete + recreate)"
	case actionUnmark:
		return "Unmark (remove from state file)"
	}
	return "?"
}

// destructive actions get a stronger confirmation colour.
func (a actionKind) destructive() bool {
	return a == actionRepublish || a == actionUnmark
}

type pendingAction struct {
	kind actionKind
	slug string
}

// Bubble Tea messages for the async write flow.
type stepMsg string          // one progress step emitted by the running op
type stepsClosedMsg struct{} // the op closed the progress channel
type doneMsg struct {
	text   string
	err    error
	reload bool // re-scan the table after a successful state-changing op
}

// chanReporter turns Reporter progress steps into messages the TUI can render
// live. The op runs in a tea.Cmd goroutine; steps flow over the buffered channel.
type chanReporter struct{ ch chan string }

func (r chanReporter) Stepf(format string, args ...any) {
	r.ch <- fmt.Sprintf(format, args...)
}

type uiModel struct {
	root         string
	baseURL      string
	mentions     map[string]string
	all          bool
	tbl          table.Model
	posts        map[string]post
	report       StatusReport
	width        int
	height       int
	previewLines int
	err          error

	mode      uiMode
	pending   pendingAction
	steps     []string
	result    string
	resultErr bool
	stepCh    chan string

	picker  filepicker.Model
	pickErr string
}

func newUIModel(root, baseURL string) (uiModel, error) {
	m := uiModel{root: root, baseURL: baseURL, mentions: configuredMentions(), previewLines: 8}
	cols := []table.Column{
		{Title: "SLUG", Width: 34},
		{Title: "DATE", Width: 10},
		{Title: "STATE", Width: 12},
		{Title: "ACTION", Width: 26},
	}
	t := table.New(table.WithColumns(cols), table.WithFocused(true), table.WithHeight(12))
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(lipgloss.Color("63")).BorderBottom(true)
	s.Selected = s.Selected.Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("63"))
	t.SetStyles(s)
	m.tbl = t

	fp := filepicker.New()
	fp.DirAllowed = true // we select a directory (the Hugo root), not a file
	fp.FileAllowed = false
	fp.ShowSize = false
	fp.AutoHeight = false
	fp.SetHeight(15)
	m.picker = fp

	// No usable repo yet → open the picker instead of failing.
	if root == "" {
		m.mode = modePickRepo
		m.picker.CurrentDirectory = startDir("")
		return m, nil
	}
	if err := m.reload(); err != nil {
		return m, err
	}
	return m, nil
}

// startDir picks a sensible directory for the repo picker to open in: the parent
// of a hint path if given, else the current working directory.
func startDir(hint string) string {
	if hint != "" {
		if abs, err := filepath.Abs(filepath.Dir(hint)); err == nil {
			return abs
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// enterPickMode switches to the repo picker, opening at the hint's parent dir.
func (m uiModel) enterPickMode(hint string) (tea.Model, tea.Cmd) {
	m.mode = modePickRepo
	m.pickErr = ""
	m.picker.CurrentDirectory = startDir(hint)
	m.layout()
	return m, m.picker.Init()
}

// reload re-scans the repo and the state file and rebuilds the table.
func (m *uiModel) reload() error {
	posts, err := scanPosts(m.root)
	if err != nil {
		return err
	}
	m.posts = make(map[string]post, len(posts))
	for _, p := range posts {
		m.posts[p.Slug] = p
	}
	rep, err := buildStatusReport(m.root, m.all, time.Now())
	if err != nil {
		return err
	}
	m.report = rep

	rows := make([]table.Row, 0, len(rep.Rows))
	for _, r := range rep.Rows {
		rows = append(rows, table.Row{r.Slug, formatDate(r.PostDate), r.Status.label(), r.Action})
	}
	m.tbl.SetRows(rows)
	return nil
}

func (m *uiModel) layout() {
	if m.height == 0 {
		return
	}
	preview := min(max(m.height/3, 5), 14)
	m.previewLines = preview
	m.tbl.SetHeight(max(m.height-preview-6, 3))
	m.picker.SetHeight(max(m.height-6, 3))
}

func (m uiModel) selectedSlug() (string, bool) {
	sel := m.tbl.SelectedRow()
	if len(sel) == 0 {
		return "", false
	}
	return sel[0], true
}

func (m uiModel) Init() tea.Cmd {
	if m.mode == modePickRepo {
		return m.picker.Init()
	}
	return nil
}

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.mode == modePickRepo {
		return m.updatePicker(msg)
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case stepMsg:
		m.steps = append(m.steps, string(msg))
		return m, waitForStep(m.stepCh)
	case stepsClosedMsg:
		return m, nil
	case doneMsg:
		m.mode = modeResult
		if msg.err != nil {
			m.result, m.resultErr = msg.err.Error(), true
		} else {
			m.result, m.resultErr = msg.text, false
			if msg.reload {
				if err := m.reload(); err != nil {
					m.result += "\n(table reload failed: " + err.Error() + ")"
				}
			}
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if m.mode == modeBrowse {
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd
	}
	return m, nil
}

// updatePicker drives the repo file picker. On selecting a directory it is
// validated as a Hugo root (must contain content/posts/); a valid pick loads
// the table and switches to browse, an invalid one shows an inline error.
func (m uiModel) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)
	if ok, path := m.picker.DidSelectFile(msg); ok {
		if root, err := resolveRepoRoot(path); err != nil {
			m.pickErr = err.Error()
		} else {
			m.root, m.pickErr = root, ""
			if e := m.reload(); e != nil {
				m.err = e
			} else {
				m.mode = modeBrowse
				m.layout()
			}
		}
	}
	return m, cmd
}

func (m uiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}

	switch m.mode {
	case modeResult:
		if key == "q" {
			return m, tea.Quit
		}
		m.mode = modeBrowse
		return m, nil

	case modeRunning:
		return m, nil // input ignored while an op is in flight

	case modeConfirm:
		switch key {
		case "y", "Y":
			return m.startAction(m.pending)
		default:
			m.mode = modeBrowse // n / N / esc / anything cancels
			return m, nil
		}

	case modeBrowse:
		switch key {
		case "q", "esc":
			return m, tea.Quit
		case "a":
			m.all = !m.all
			if err := m.reload(); err != nil {
				m.err = err
			}
			return m, nil
		case "r":
			if err := m.reload(); err != nil {
				m.err = err
			}
			return m, nil
		case "c":
			return m.enterPickMode(m.root)
		case "d":
			if slug, ok := m.selectedSlug(); ok {
				return m.startAction(pendingAction{kind: actionDryRun, slug: slug})
			}
			return m, nil
		case "p", "e", "R", "u":
			if slug, ok := m.selectedSlug(); ok {
				m.pending = pendingAction{kind: keyToAction(key), slug: slug}
				m.mode = modeConfirm
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.tbl, cmd = m.tbl.Update(msg)
		return m, cmd
	}
	return m, nil
}

func keyToAction(key string) actionKind {
	switch key {
	case "p":
		return actionPublish
	case "e":
		return actionEdit
	case "R":
		return actionRepublish
	case "u":
		return actionUnmark
	}
	return actionDryRun
}

// startAction moves to the running state and launches the op plus the
// step-listener concurrently.
func (m uiModel) startAction(act pendingAction) (tea.Model, tea.Cmd) {
	m.pending = act
	m.mode = modeRunning
	m.steps = nil
	m.result = ""
	m.stepCh = make(chan string, 64)
	return m, tea.Batch(m.runActionCmd(act, m.stepCh), waitForStep(m.stepCh))
}

// waitForStep reads one progress step and re-arms itself; a closed channel ends
// the listener.
func waitForStep(ch chan string) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return stepsClosedMsg{}
		}
		return stepMsg(s)
	}
}

// runActionCmd runs the selected core operation in a tea.Cmd goroutine, feeding
// progress through ch and returning a doneMsg. These are the exact same core
// functions the CLI calls.
func (m uiModel) runActionCmd(act pendingAction, ch chan string) tea.Cmd {
	root, baseURL, mentions := m.root, m.baseURL, m.mentions
	return func() tea.Msg {
		rep := chanReporter{ch: ch}
		defer close(ch)
		switch act.kind {
		case actionDryRun:
			res, err := runPublish(root, act.slug, "", true, true, false, baseURL, mentions, rep)
			if err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: dryRunSummary(res)}
		case actionPublish:
			res, err := runPublish(root, act.slug, "", false, false, false, baseURL, mentions, rep)
			if err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: publishSummary(res), reload: true}
		case actionEdit:
			urn, err := runEdit(root, act.slug, mentions)
			if err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: fmt.Sprintf("edited %s commentary (URN: %s)", act.slug, urn), reload: true}
		case actionRepublish:
			res, err := runRepublish(root, act.slug, "", false, baseURL, mentions, rep)
			if err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: publishSummary(res), reload: true}
		case actionUnmark:
			if err := unmarkPost(root, act.slug); err != nil {
				return doneMsg{err: err}
			}
			return doneMsg{text: fmt.Sprintf("removed %s from %s", act.slug, stateFileName), reload: true}
		}
		return doneMsg{err: fmt.Errorf("unknown action")}
	}
}

func dryRunSummary(res PublishResult) string {
	when := "immediately"
	if res.Scheduled {
		when = "scheduled for " + formatDateTime(res.PublishAt)
	}
	img := "with thumbnail"
	if res.FeaturedPath == "" {
		img = "WITHOUT image"
	}
	return fmt.Sprintf("dry-run OK — preflight passed; would publish %s, %s", when, img)
}

func publishSummary(res PublishResult) string {
	if res.Scheduled {
		return fmt.Sprintf("scheduled %s for %s (URN: %s)", res.Slug, formatDateTime(res.PublishAt), res.URN)
	}
	return fmt.Sprintf("published %s (URN: %s)", res.Slug, res.URN)
}

func (m uiModel) View() string {
	if m.err != nil {
		return uiErrStyle.Render("error: "+m.err.Error()) + "\n\npress q to quit\n"
	}
	if m.mode == modePickRepo {
		return m.pickerView()
	}
	title := uiTitleStyle.Render("li-sync · " + m.root)
	box := uiPreviewBox.Width(max(m.width-2, 20)).Render(m.bottomContent())
	footer := uiFooterStyle.Render(m.footerText())
	return lipgloss.JoinVertical(lipgloss.Left, title, m.tbl.View(), box, footer)
}

// bottomContent renders the panel under the table: companion preview while
// browsing, or the confirm/running/result of an action.
func (m uiModel) bottomContent() string {
	switch m.mode {
	case modeConfirm:
		verb := uiTitleBold.Render(m.pending.kind.verb())
		if m.pending.kind.destructive() {
			verb = uiWarnStyle.Render(m.pending.kind.verb())
		}
		return fmt.Sprintf("%s  →  %s\n\n[y] confirm    [n] cancel", verb, m.pending.slug)
	case modeRunning:
		body := strings.Join(m.steps, "\n")
		if body == "" {
			body = "working…"
		}
		return fmt.Sprintf("running: %s %s\n\n%s", m.pending.kind.verb(), m.pending.slug, body)
	case modeResult:
		head := uiOkStyle.Render("✓ done")
		if m.resultErr {
			head = uiErrStyle.Render("✗ failed")
		}
		return truncateLines(head+"\n\n"+m.result, m.previewLines) + "\n\n(press any key to continue)"
	default:
		return m.previewText()
	}
}

func (m uiModel) footerText() string {
	scope := "actionable"
	if m.all {
		scope = "all"
	}
	switch m.mode {
	case modeConfirm:
		return uiFooterStyle.Render("confirm the action above — [y] yes   [n] no")
	case modeRunning:
		return uiFooterStyle.Render("working… please wait")
	case modeResult:
		return uiFooterStyle.Render("press any key to return to the table")
	default:
		return fmt.Sprintf(
			"%d shown · %d pending · %d hidden · view:%s\n[↑↓] move · [d]ry-run [p]ublish [e]dit [R]epublish [u]nmark · [a]ll [r]eload [c]hange-repo [q]uit",
			len(m.report.Rows), m.report.Pending, m.report.Hidden, scope,
		)
	}
}

func (m uiModel) pickerView() string {
	var b strings.Builder
	b.WriteString(uiTitleStyle.Render("li-sync · select your Hugo site root") + "\n\n")
	b.WriteString(uiFooterStyle.Render("pick a directory that contains content/posts/   ·   [enter] choose   [h/esc] up   [ctrl+c] quit") + "\n")
	if m.pickErr != "" {
		b.WriteString(uiErrStyle.Render("✗ "+m.pickErr) + "\n")
	}
	b.WriteString("\n" + m.picker.View() + "\n")
	b.WriteString(uiFooterStyle.Render("in: " + m.picker.CurrentDirectory))
	return b.String()
}

func (m uiModel) previewText() string {
	sel := m.tbl.SelectedRow()
	if len(sel) == 0 {
		return "(no posts to show — press [a] to include future/draft/no-companion)"
	}
	slug := sel[0]
	p, ok := m.posts[slug]
	if !ok {
		return "(no data for " + slug + ")"
	}

	var b strings.Builder
	if p.Title != "" {
		b.WriteString(uiTitleBold.Render(p.Title) + "\n")
	}
	for _, r := range m.report.Rows {
		if r.Slug == slug {
			st := lipgloss.NewStyle().Foreground(stateColor(r.Status)).Render(r.Status.label())
			fmt.Fprintf(&b, "%s    %s\n", st, formatDate(r.PostDate))
			break
		}
	}
	fmt.Fprintf(&b, "%s/posts/%s/\n", m.baseURL, p.URLSlug)

	if p.HasCompanion {
		data, err := os.ReadFile(p.CompanionPath)
		if err != nil {
			fmt.Fprintf(&b, "\n(could not read companion: %v)", err)
		} else {
			b.WriteString("\n" + strings.TrimSpace(string(data)))
		}
	} else {
		b.WriteString("\n(no linkedin-post.txt companion)")
	}
	return truncateLines(b.String(), m.previewLines)
}

func truncateLines(s string, max int) string {
	if max < 1 {
		max = 1
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	return strings.Join(lines[:max], "\n") + "\n…"
}

// runTUI launches the interactive dashboard. It requires a terminal; automated
// callers should use the CLI subcommands instead.
func runTUI(root, baseURL string) error {
	m, err := newUIModel(root, baseURL)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
