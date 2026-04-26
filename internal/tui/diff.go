package tui

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type diffViewState struct {
	branch   string
	files    []fileDiff
	fileIdx  int
	vp       viewport.Model
	focus    int  // 0=file list, 1=viewport
	prevMode mode // where to return on esc
	ready    bool
}

type fileDiff struct {
	Path    string
	Added   int
	Deleted int
	Lines   []diffLine
}

type diffLine struct {
	Kind byte // 'h'=header, '@'=hunk, '+'=add, '-'=del, ' '=context
	Text string
}

var (
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("51"))
	diffHdrStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	diffCtxStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	fileSelStyle  = lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("230"))
	pathStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	addCntStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	delCntStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	paneBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	paneFocused   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("205")).Padding(0, 1)
)

func (m model) enterDiffView(branch string, prev mode) (tea.Model, tea.Cmd) {
	files, err := parseDiff(m.repoRoot, "main", branch)
	if err != nil {
		m.flash = "diff: " + err.Error()
		return m, nil
	}
	if len(files) == 0 {
		m.flash = "no changes on " + branch
		return m, nil
	}
	w, h := m.width, m.height
	if w == 0 {
		w, h = 100, 30
	}
	left := w / 3
	if left < 30 {
		left = 30
	}
	right := w - left - 6
	if right < 30 {
		right = 30
	}
	vp := viewport.New(right, h-6)
	m.diff = diffViewState{
		branch:   branch,
		files:    files,
		fileIdx:  0,
		vp:       vp,
		focus:    0,
		prevMode: prev,
		ready:    true,
	}
	m.diff.vp.SetContent(renderFileDiff(files[0], right))
	m.mode = modeDiffView
	m.flash = ""
	return m, nil
}

func (m model) updateDiffView(msg tea.Msg) (tea.Model, tea.Cmd) {
	if !m.diff.ready {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w, h := msg.Width, msg.Height
		left := w / 3
		if left < 30 {
			left = 30
		}
		right := w - left - 6
		if right < 30 {
			right = 30
		}
		m.diff.vp.Width = right
		m.diff.vp.Height = h - 6
		if m.diff.fileIdx < len(m.diff.files) {
			m.diff.vp.SetContent(renderFileDiff(m.diff.files[m.diff.fileIdx], right))
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc":
			m.mode = m.diff.prevMode
			return m, nil
		case "tab":
			m.diff.focus = 1 - m.diff.focus
			return m, nil
		}
		if m.diff.focus == 0 {
			return m.diffViewMoveFile(msg)
		}
		var cmd tea.Cmd
		m.diff.vp, cmd = m.diff.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) diffViewMoveFile(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "j", "down":
		if m.diff.fileIdx < len(m.diff.files)-1 {
			m.diff.fileIdx++
			m.diff.vp.SetContent(renderFileDiff(m.diff.files[m.diff.fileIdx], m.diff.vp.Width))
			m.diff.vp.GotoTop()
		}
	case "k", "up":
		if m.diff.fileIdx > 0 {
			m.diff.fileIdx--
			m.diff.vp.SetContent(renderFileDiff(m.diff.files[m.diff.fileIdx], m.diff.vp.Width))
			m.diff.vp.GotoTop()
		}
	case "enter", "l", "right":
		m.diff.focus = 1
	}
	return m, nil
}

func (m model) viewDiffView() string {
	if !m.diff.ready || len(m.diff.files) == 0 {
		return "no diff"
	}
	w, h := m.width, m.height
	if w == 0 {
		w, h = 100, 30
	}
	left := w / 3
	if left < 30 {
		left = 30
	}

	var lb strings.Builder
	header := titleStyle.Render("▎ " + m.diff.branch)
	totalAdd, totalDel := 0, 0
	for _, f := range m.diff.files {
		totalAdd += f.Added
		totalDel += f.Deleted
	}
	statsLine := fmt.Sprintf("%d files  %s %s",
		len(m.diff.files),
		addCntStyle.Render(fmt.Sprintf("+%d", totalAdd)),
		delCntStyle.Render(fmt.Sprintf("-%d", totalDel)))
	lb.WriteString(header + "\n")
	lb.WriteString(dim.Render(statsLine) + "\n\n")

	maxPath := left - 14
	if maxPath < 10 {
		maxPath = 10
	}
	for i, f := range m.diff.files {
		row := fmt.Sprintf("%-*s  %s %s",
			maxPath, truncate(f.Path, maxPath),
			addCntStyle.Render(fmt.Sprintf("+%d", f.Added)),
			delCntStyle.Render(fmt.Sprintf("-%d", f.Deleted)))
		if i == m.diff.fileIdx {
			row = fileSelStyle.Render(row)
		} else {
			row = pathStyle.Render(row)
		}
		lb.WriteString(row + "\n")
	}

	leftStyle := paneBorder
	rightStyle := paneBorder
	if m.diff.focus == 0 {
		leftStyle = paneFocused
	} else {
		rightStyle = paneFocused
	}

	leftPane := leftStyle.Width(left).Height(h - 4).Render(lb.String())
	rightPane := rightStyle.Width(w - left - 4).Height(h - 4).Render(m.diff.vp.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	footer := dim.Render("tab focus · j/k file or scroll · enter→viewer · esc back · q quit")
	return body + "\n" + footer
}

func (m model) viewDiffSummary() string {
	if len(m.diffEntries) == 0 {
		return dim.Render("no agent branches yet (q to quit)")
	}
	w := m.width
	if w == 0 {
		w = 100
	}
	cardW := w - 6
	if cardW < 60 {
		cardW = 60
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("saturn — all agent diffs") + "\n")
	b.WriteString(dim.Render("j/k navigate · enter open · r refresh · esc back · q quit") + "\n\n")

	for i, e := range m.diffEntries {
		var inner strings.Builder
		title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")).Render("▎ " + e.Branch)
		if e.NoMain {
			inner.WriteString(title + "  " + dim.Render("(no main branch)") + "\n")
		} else {
			fileWord := "files"
			if len(e.Files) == 1 {
				fileWord = "file"
			}
			stats := fmt.Sprintf("%d %s   %s   %s",
				len(e.Files), fileWord,
				addCntStyle.Render(fmt.Sprintf("+%d", e.Added)),
				delCntStyle.Render(fmt.Sprintf("-%d", e.Deleted)))
			inner.WriteString(title + "  " + dim.Render(stats) + "\n")
			if len(e.Files) == 0 {
				inner.WriteString(dim.Render("  (no changes yet)") + "\n")
			} else {
				maxShow := 5
				if len(e.Files) < maxShow {
					maxShow = len(e.Files)
				}
				for _, f := range e.Files[:maxShow] {
					inner.WriteString(fmt.Sprintf("  %s %s %s\n",
						pathStyle.Render(truncate(f.Path, cardW-20)),
						addCntStyle.Render(fmt.Sprintf("+%d", f.Added)),
						delCntStyle.Render(fmt.Sprintf("-%d", f.Deleted))))
				}
				if len(e.Files) > maxShow {
					inner.WriteString(dim.Render(fmt.Sprintf("  …%d more", len(e.Files)-maxShow)) + "\n")
				}
			}
		}

		card := paneBorder
		if i == m.diffCursor {
			card = paneFocused
		}
		b.WriteString(card.Width(cardW).Render(inner.String()) + "\n")
	}
	return b.String()
}

func parseDiff(repoRoot, base, branch string) ([]fileDiff, error) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", base+".."+branch)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return splitFiles(string(out)), nil
}

func splitFiles(in string) []fileDiff {
	var files []fileDiff
	var cur *fileDiff
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}
	for _, ln := range strings.Split(in, "\n") {
		if strings.HasPrefix(ln, "diff --git ") {
			flush()
			cur = &fileDiff{Path: extractPath(ln)}
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(ln, "diff --git "),
			strings.HasPrefix(ln, "index "),
			strings.HasPrefix(ln, "--- "),
			strings.HasPrefix(ln, "+++ "),
			strings.HasPrefix(ln, "new file"),
			strings.HasPrefix(ln, "deleted file"),
			strings.HasPrefix(ln, "rename "),
			strings.HasPrefix(ln, "similarity "),
			strings.HasPrefix(ln, "old mode"),
			strings.HasPrefix(ln, "new mode"):
			cur.Lines = append(cur.Lines, diffLine{Kind: 'h', Text: ln})
		case strings.HasPrefix(ln, "@@"):
			cur.Lines = append(cur.Lines, diffLine{Kind: '@', Text: ln})
		case strings.HasPrefix(ln, "+"):
			cur.Added++
			cur.Lines = append(cur.Lines, diffLine{Kind: '+', Text: ln})
		case strings.HasPrefix(ln, "-"):
			cur.Deleted++
			cur.Lines = append(cur.Lines, diffLine{Kind: '-', Text: ln})
		default:
			cur.Lines = append(cur.Lines, diffLine{Kind: ' ', Text: ln})
		}
	}
	flush()
	return files
}

func extractPath(diffHeader string) string {
	parts := strings.Fields(diffHeader)
	if len(parts) >= 4 {
		return strings.TrimPrefix(parts[3], "b/")
	}
	return "?"
}

func renderFileDiff(f fileDiff, width int) string {
	var b strings.Builder
	b.WriteString(diffHdrStyle.Render(f.Path) + "\n")
	b.WriteString(dim.Render(fmt.Sprintf("  +%d  -%d", f.Added, f.Deleted)) + "\n\n")
	for _, ln := range f.Lines {
		text := ln.Text
		if width > 0 && len(text) > width-2 {
			text = text[:width-2] + "…"
		}
		switch ln.Kind {
		case 'h':
			b.WriteString(diffHdrStyle.Render(text) + "\n")
		case '@':
			b.WriteString(diffHunkStyle.Render(text) + "\n")
		case '+':
			b.WriteString(diffAddStyle.Render(text) + "\n")
		case '-':
			b.WriteString(diffDelStyle.Render(text) + "\n")
		default:
			b.WriteString(diffCtxStyle.Render(text) + "\n")
		}
	}
	return b.String()
}
