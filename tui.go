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
	modeSelectPlatform
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
	li   bool // run on LinkedIn
	x    bool // run on X
}

// platforms renders the selected targets, e.g. "LinkedIn + X".
func (a pendingAction) platforms() string {
	switch {
	case a.li && a.x:
		return "LinkedIn + X"
	case a.li:
		return "LinkedIn"
	case a.x:
		return "X"
	}
	return "nothing"
}

// Bubble Tea messages for the async write flow.
type stepMsg string          // one progress step emitted by the running op
type stepsClosedMsg struct{} // the op closed the progress channel
type doneMsg struct {
	text   string
	err    error
	failed bool // at least one platform's op errored (multi-platform path)
	reload bool // re-scan the table after a successful state-changing op
}

// chanReporter turns Reporter progress steps into messages the TUI can render
// live. The op runs in a tea.Cmd goroutine; steps flow over the buffered channel.
type chanReporter struct{ ch chan string }

func (r chanReporter) Stepf(format string, args ...any) {
	r.ch <- fmt.Sprintf(format, args...)
}

// prefixReporter tags every step with a platform marker ("[LinkedIn] " / "[X] ")
// so a multi-platform run interleaves legibly over the single shared channel.
type prefixReporter struct {
	ch     chan string
	prefix string
}

func (r prefixReporter) Stepf(format string, args ...any) {
	r.ch <- r.prefix + fmt.Sprintf(format, args...)
}

type uiModel struct {
	root         string
	baseURL      string
	mentions     map[string]string
	all          bool
	tbl          table.Model
	posts        map[string]post
	liRows       map[string]row // slug → LinkedIn classification
	xRows        map[string]row // slug → X classification
	shown        int            // rows currently in the table
	liPending    int            // posts MISSING on LinkedIn
	xPending     int            // posts MISSING on X
	hidden       int            // rows filtered out when all == false
	width        int
	height       int
	previewLines int
	err          error

	mode      uiMode
	pending   pendingAction
	selLI     bool // selector: LinkedIn toggled on
	selX      bool // selector: X toggled on
	availLI   bool // selector: LinkedIn is a valid target for the pending action
	availX    bool // selector: X is a valid target for the pending action
	steps     []string
	result    string
	resultErr bool
	stepCh    chan string

	picker  filepicker.Model
	pickErr string
}

func newUIModel(root, baseURL, pickerHint string) (uiModel, error) {
	m := uiModel{root: root, baseURL: baseURL, mentions: configuredMentions(), previewLines: 8}
	cols := []table.Column{
		{Title: "SLUG", Width: 34},
		{Title: "DATE", Width: 10},
		{Title: "LINKEDIN", Width: 12},
		{Title: "X", Width: 12},
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

	// No usable repo yet → open the picker instead of failing, starting near the
	// last repo opened (pickerHint) when there is one.
	if root == "" {
		m.mode = modePickRepo
		m.picker.CurrentDirectory = startDir(pickerHint)
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

// hiddenByDefault reports whether a status is filtered out of the actionable
// view (mirrors buildStatusReport's hideByDefault).
func hiddenByDefault(s rowStatus) bool {
	return s == statusFuture || s == statusDraft || s == statusNoCompanion
}

// reload re-scans the repo and both state files, then rebuilds the dual table.
// A post is shown in the actionable view if it is visible on EITHER platform;
// `all` reveals everything. Both reports are built with all=true so every post
// is classified on both platforms, then merged by slug.
func (m *uiModel) reload() error {
	posts, err := scanPosts(m.root)
	if err != nil {
		return err
	}
	m.posts = make(map[string]post, len(posts))
	for _, p := range posts {
		m.posts[p.Slug] = p
	}

	now := time.Now()
	liRep, err := buildStatusReport(m.root, true, now)
	if err != nil {
		return err
	}
	xRep, err := buildXStatusReport(m.root, true, now)
	if err != nil {
		return err
	}
	m.liRows = make(map[string]row, len(liRep.Rows))
	for _, r := range liRep.Rows {
		m.liRows[r.Slug] = r
	}
	m.xRows = make(map[string]row, len(xRep.Rows))
	for _, r := range xRep.Rows {
		m.xRows[r.Slug] = r
	}

	// liRep (all=true) lists every post in scan order; it is the row spine.
	m.shown, m.liPending, m.xPending, m.hidden = 0, 0, 0, 0
	rows := make([]table.Row, 0, len(liRep.Rows))
	for _, li := range liRep.Rows {
		x := m.xRows[li.Slug]
		if li.Status == statusMissing {
			m.liPending++
		}
		if x.Status == statusMissing {
			m.xPending++
		}
		if !m.all && hiddenByDefault(li.Status) && hiddenByDefault(x.Status) {
			m.hidden++
			continue
		}
		m.shown++
		rows = append(rows, table.Row{li.Slug, formatDate(li.PostDate), li.Status.label(), x.Status.label()})
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
			return m, nil
		}
		m.result, m.resultErr = msg.text, msg.failed
		if msg.reload {
			if err := m.reload(); err != nil {
				m.result += "\n(table reload failed: " + err.Error() + ")"
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
		if k := msg.String(); k == "q" || k == "ctrl+c" {
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
				_ = saveLastRepo(root) // remember this choice for next launch
				m.mode = modeBrowse
				m.layout()
			}
		}
	}
	return m, cmd
}

func (m uiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	// q (and ctrl+c) always quit the app — except while an op is running, where
	// input is ignored so a stray key can't abandon an in-flight publish.
	if (key == "q" || key == "ctrl+c") && m.mode != modeRunning {
		return m, tea.Quit
	}

	switch m.mode {
	case modeResult:
		m.mode = modeBrowse // any other key dismisses the result
		return m, nil

	case modeRunning:
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil // other input ignored while an op is in flight

	case modeSelectPlatform:
		switch key {
		case "l", "L":
			if m.availLI {
				m.selLI = !m.selLI
			}
			return m, nil
		case "x", "X":
			if m.availX {
				m.selX = !m.selX
			}
			return m, nil
		case "enter":
			if !m.selLI && !m.selX {
				return m, nil // nothing selected — stay
			}
			m.pending.li, m.pending.x = m.selLI, m.selX
			if m.pending.kind == actionDryRun {
				return m.startAction(m.pending) // dry-run is harmless, skip confirm
			}
			m.mode = modeConfirm
			return m, nil
		default:
			m.mode = modeBrowse // n / N / esc / anything cancels
			return m, nil
		}

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
		case "esc":
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
		case "e":
			// Edit has no X equivalent — LinkedIn only, straight to confirm.
			if slug, ok := m.selectedSlug(); ok {
				m.pending = pendingAction{kind: actionEdit, slug: slug, li: true}
				m.mode = modeConfirm
			}
			return m, nil
		case "d", "p", "R", "u":
			if slug, ok := m.selectedSlug(); ok {
				return m.enterSelect(keyToAction(key), slug)
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

// enterSelect opens the platform checkbox selector for a write action. It
// derives each platform's availability from that post's classification:
//   - publish / dry-run: the platform must have a companion (linkedin-post.txt
//     or x-post.txt), i.e. its status is not "no companion".
//   - republish / unmark: the platform must already have a state entry
//     (LinkedIn scheduled/published, X published) — there's nothing to redo or
//     remove otherwise.
//
// Available platforms start pre-selected. If neither is available the key is a
// no-op (stay in browse).
func (m uiModel) enterSelect(kind actionKind, slug string) (tea.Model, tea.Cmd) {
	li := m.liRows[slug]
	x := m.xRows[slug]
	switch kind {
	case actionPublish, actionDryRun:
		m.availLI = li.Status != statusNoCompanion && li.Status != statusDraft
		m.availX = x.Status != statusNoCompanion && x.Status != statusDraft
	case actionRepublish, actionUnmark:
		m.availLI = li.Status == statusScheduled || li.Status == statusPublished
		m.availX = x.Status == statusPublished
	}
	if !m.availLI && !m.availX {
		return m, nil // nothing actionable on either platform
	}
	m.pending = pendingAction{kind: kind, slug: slug}
	m.selLI, m.selX = m.availLI, m.availX
	m.mode = modeSelectPlatform
	return m, nil
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

// runActionCmd runs the selected action on each chosen platform sequentially in
// a tea.Cmd goroutine, feeding progress through ch and returning an aggregated
// doneMsg. These are the exact same core functions the CLI calls; the X ones
// are presentation-free counterparts of the LinkedIn ones. Each platform's
// outcome is collected independently — one failing doesn't abort the other.
func (m uiModel) runActionCmd(act pendingAction, ch chan string) tea.Cmd {
	root, baseURL, mentions := m.root, m.baseURL, m.mentions
	return func() tea.Msg {
		defer close(ch)
		var parts []string
		failed, reload := false, false
		ok := func(s string) { parts = append(parts, uiOkStyle.Render("✓")+" "+s) }
		bad := func(label string, err error) {
			parts = append(parts, uiErrStyle.Render("✗")+" "+label+": "+err.Error())
			failed = true
		}

		if act.li {
			rep := prefixReporter{ch: ch, prefix: "[LinkedIn] "}
			switch act.kind {
			case actionDryRun:
				if res, err := runPublish(root, act.slug, "", true, true, false, baseURL, mentions, rep); err != nil {
					bad("LinkedIn dry-run", err)
				} else {
					ok("LinkedIn — " + dryRunSummary(res))
				}
			case actionPublish:
				if res, err := runPublish(root, act.slug, "", false, false, false, baseURL, mentions, rep); err != nil {
					bad("LinkedIn publish", err)
				} else {
					ok("LinkedIn — " + publishSummary(res))
					reload = true
				}
			case actionEdit:
				if urn, err := runEdit(root, act.slug, mentions); err != nil {
					bad("LinkedIn edit", err)
				} else {
					ok(fmt.Sprintf("LinkedIn — edited %s commentary (URN: %s)", act.slug, urn))
					reload = true
				}
			case actionRepublish:
				if res, err := runRepublish(root, act.slug, "", false, baseURL, mentions, rep); err != nil {
					bad("LinkedIn republish", err)
				} else {
					ok("LinkedIn — " + publishSummary(res))
					reload = true
				}
			case actionUnmark:
				if err := unmarkPost(root, act.slug); err != nil {
					bad("LinkedIn unmark", err)
				} else {
					ok(fmt.Sprintf("LinkedIn — removed %s from %s", act.slug, stateFileName))
					reload = true
				}
			}
		}

		if act.x {
			rep := prefixReporter{ch: ch, prefix: "[X] "}
			switch act.kind {
			case actionDryRun:
				if res, err := runXPublish(root, act.slug, true, true, false, baseURL, rep); err != nil {
					bad("X dry-run", err)
				} else {
					ok(fmt.Sprintf("X — dry-run OK; would tweet (~%d weighted chars)", tweetLen(res.Text)))
				}
			case actionPublish:
				if res, err := runXPublish(root, act.slug, false, false, false, baseURL, rep); err != nil {
					bad("X publish", err)
				} else {
					ok(fmt.Sprintf("X — tweeted %s (id %s)", res.Slug, res.TweetID))
					reload = true
				}
			case actionRepublish:
				if res, err := runXRepublish(root, act.slug, false, baseURL, rep); err != nil {
					bad("X republish", err)
				} else {
					ok(fmt.Sprintf("X — tweeted %s (id %s)", res.Slug, res.TweetID))
					reload = true
				}
			case actionUnmark:
				if err := runXUnmark(root, act.slug); err != nil {
					bad("X unmark", err)
				} else {
					ok(fmt.Sprintf("X — removed %s from %s", act.slug, xStateFileName))
					reload = true
				}
			case actionEdit:
				bad("X edit", fmt.Errorf("X has no edit endpoint — use republish"))
			}
		}

		if len(parts) == 0 {
			return doneMsg{err: fmt.Errorf("no platform selected")}
		}
		return doneMsg{text: strings.Join(parts, "\n\n"), failed: failed, reload: reload}
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
	// Fixed-height box: Width makes long companion lines wrap INSIDE the panel
	// (not into the terminal), Height pads short content, and MaxHeight hard-clips
	// tall content. Without this the panel grows with the selected post and shoves
	// the table header off-screen as you move through the list.
	box := uiPreviewBox.
		Width(max(m.width-4, 16)).
		Height(m.previewLines).
		MaxHeight(m.previewLines + 2).
		Render(m.bottomContent())
	footer := uiFooterStyle.Render(m.footerText())
	return lipgloss.JoinVertical(lipgloss.Left, title, m.tbl.View(), box, footer)
}

// bottomContent renders the panel under the table: companion preview while
// browsing, or the confirm/running/result of an action.
func (m uiModel) bottomContent() string {
	switch m.mode {
	case modeSelectPlatform:
		return m.selectPlatformContent()
	case modeConfirm:
		verb := uiTitleBold.Render(m.pending.kind.verb())
		if m.pending.kind.destructive() {
			verb = uiWarnStyle.Render(m.pending.kind.verb())
		}
		return fmt.Sprintf("%s  →  %s\non: %s\n\n[y] confirm    [n] cancel",
			verb, m.pending.slug, uiTitleBold.Render(m.pending.platforms()))
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

// selectPlatformContent renders the platform checkbox selector. Unavailable
// platforms render as a dimmed "[-]" and cannot be toggled on.
func (m uiModel) selectPlatformContent() string {
	verb := uiTitleBold.Render(m.pending.kind.verb())
	if m.pending.kind.destructive() {
		verb = uiWarnStyle.Render(m.pending.kind.verb())
	}
	line := func(sel, avail bool, name string, st rowStatus) string {
		switch {
		case !avail:
			return uiFooterStyle.Render(fmt.Sprintf("  [-] %-9s %s (unavailable)", name, st.label()))
		case sel:
			return fmt.Sprintf("  [%s] %-9s %s", uiOkStyle.Render("✓"), name, st.label())
		default:
			return fmt.Sprintf("  [ ] %-9s %s", name, st.label())
		}
	}
	li := m.liRows[m.pending.slug]
	x := m.xRows[m.pending.slug]
	body := fmt.Sprintf("%s  →  %s\n\n%s\n%s\n\n[l] toggle LinkedIn   [x] toggle X   [enter] continue   [esc] cancel",
		verb, m.pending.slug,
		line(m.selLI, m.availLI, "LinkedIn", li.Status),
		line(m.selX, m.availX, "X", x.Status),
	)
	return body
}

func (m uiModel) footerText() string {
	scope := "actionable"
	if m.all {
		scope = "all"
	}
	switch m.mode {
	case modeSelectPlatform:
		return uiFooterStyle.Render("choose targets — [l]/[x] toggle   [enter] continue   [esc] cancel")
	case modeConfirm:
		return uiFooterStyle.Render("confirm the action above — [y] yes   [n] no")
	case modeRunning:
		return uiFooterStyle.Render("working… please wait")
	case modeResult:
		return uiFooterStyle.Render("press any key to return to the table")
	default:
		return fmt.Sprintf(
			"%d shown · LI %d pending · X %d pending · %d hidden · view:%s\n[↑↓] move · [d]ry-run [p]ublish [e]dit(LI) [R]epublish [u]nmark · [a]ll [r]eload [c]hange-repo [q]uit",
			m.shown, m.liPending, m.xPending, m.hidden, scope,
		)
	}
}

func (m uiModel) pickerView() string {
	var b strings.Builder
	b.WriteString(uiTitleStyle.Render("li-sync · select your Hugo site root") + "\n\n")
	b.WriteString(uiFooterStyle.Render("pick a directory that contains content/posts/   ·   [enter] choose   [h/esc] up   [q] quit") + "\n")
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
	li := m.liRows[slug]
	x := m.xRows[slug]
	liSt := lipgloss.NewStyle().Foreground(stateColor(li.Status)).Render(li.Status.label())
	xSt := lipgloss.NewStyle().Foreground(stateColor(x.Status)).Render(x.Status.label())
	fmt.Fprintf(&b, "LinkedIn: %s    X: %s    %s\n", liSt, xSt, formatDate(p.Date))
	fmt.Fprintf(&b, "%s/posts/%s/\n", m.baseURL, p.URLSlug)

	// Companion preview: LinkedIn's full text (the longer one), then a one-line
	// note on whether the X companion exists.
	if p.HasCompanion {
		data, err := os.ReadFile(p.CompanionPath)
		if err != nil {
			fmt.Fprintf(&b, "\n(could not read linkedin-post.txt: %v)", err)
		} else {
			b.WriteString("\n" + strings.TrimSpace(string(data)))
		}
	} else {
		b.WriteString("\n(no linkedin-post.txt companion)")
	}
	if hasXCompanion(m.root, slug) {
		b.WriteString("\n\n[x-post.txt present]")
	} else {
		b.WriteString("\n\n(no x-post.txt companion)")
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
func runTUI(root, baseURL, pickerHint string) error {
	m, err := newUIModel(root, baseURL, pickerHint)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
