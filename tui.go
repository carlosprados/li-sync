package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// This is the additive, interactive front-end. It is strictly read-only: it
// browses post/LinkedIn state and previews companions, but performs no API
// calls and no writes. Every mutating operation stays in the CLI subcommands so
// the tool remains fully scriptable by an automated agent without a TTY.

var (
	uiTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63")).Padding(0, 1)
	uiFooterStyle = lipgloss.NewStyle().Faint(true).Padding(0, 1)
	uiPreviewBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	uiErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
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

type uiModel struct {
	root         string
	baseURL      string
	all          bool
	tbl          table.Model
	posts        map[string]post
	report       StatusReport
	width        int
	height       int
	previewLines int
	err          error
}

func newUIModel(root, baseURL string) (uiModel, error) {
	m := uiModel{root: root, baseURL: baseURL, previewLines: 8}
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
	if err := m.reload(); err != nil {
		return m, err
	}
	return m, nil
}

// reload re-scans the repo and the state file and rebuilds the table. Called on
// startup, on toggle-all, and on the explicit reload key.
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
	preview := min(max(m.height/3, 4), 14)
	m.previewLines = preview
	m.tbl.SetHeight(max(m.height-preview-6, 3))
}

func (m uiModel) Init() tea.Cmd { return nil }

func (m uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
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
		}
	}
	var cmd tea.Cmd
	m.tbl, cmd = m.tbl.Update(msg)
	return m, cmd
}

func (m uiModel) View() string {
	if m.err != nil {
		return uiErrStyle.Render("error: "+m.err.Error()) + "\n\npress q to quit\n"
	}
	title := uiTitleStyle.Render("li-sync · " + m.root)
	preview := uiPreviewBox.Width(max(m.width-2, 20)).Render(m.previewText())
	footer := uiFooterStyle.Render(m.footerText())
	return lipgloss.JoinVertical(lipgloss.Left, title, m.tbl.View(), preview, footer)
}

func (m uiModel) footerText() string {
	scope := "actionable"
	if m.all {
		scope = "all"
	}
	return fmt.Sprintf(
		"%d shown · %d pending · %d hidden · view:%s    [↑/↓] move   [a] all/actionable   [r] reload   [q] quit",
		len(m.report.Rows), m.report.Pending, m.report.Hidden, scope,
	)
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
	// Colored state line, matched from the report by slug.
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
