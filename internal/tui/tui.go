package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type runInfo struct {
	ID         string
	Iterations int
	StopReason string
	EndedAt    string
	Error      string
	TailLines  []string
}

type model struct {
	root   string
	runs   []runInfo
	cursor int
	width  int
	height int
	err    error
}

type tickMsg time.Time
type refreshMsg struct {
	runs []runInfo
	err  error
}

func Run(runsRoot string) error {
	m := model{root: runsRoot}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(refreshCmd(m.root), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func refreshCmd(root string) tea.Cmd {
	return func() tea.Msg {
		runs, err := loadRuns(root)
		return refreshMsg{runs: runs, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "j", "down":
			if m.cursor < len(m.runs)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.cursor > 0 {
				m.cursor--
			}
		case "r":
			return m, refreshCmd(m.root)
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(refreshCmd(m.root), tickCmd())
	case refreshMsg:
		m.err = msg.err
		m.runs = msg.runs
		if m.cursor >= len(m.runs) {
			m.cursor = maxInt(0, len(m.runs)-1)
		}
	}
	return m, nil
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	rowSel     = lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("230"))
	rowNormal  = lipgloss.NewStyle()
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	runBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

func (m model) View() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v (press q to quit)", m.err)
	}
	if len(m.runs) == 0 {
		return titleStyle.Render("saturn watch") + "\n\n" +
			dim.Render("no runs found under "+m.root+"\n\npress q to quit")
	}

	w, h := m.width, m.height
	if w == 0 {
		w, h = 100, 30
	}

	left := w / 3
	if left < 30 {
		left = 30
	}
	right := w - left - 3
	if right < 20 {
		right = 20
	}

	var lb strings.Builder
	lb.WriteString(titleStyle.Render("saturn watch") + "\n")
	lb.WriteString(dim.Render(fmt.Sprintf("%d runs · refresh 2s · j/k r q", len(m.runs))) + "\n\n")
	for i, r := range m.runs {
		line := fmt.Sprintf("%s %-20s %s", badge(r), truncate(r.ID, 20), dim.Render(fmt.Sprintf("iter=%d", r.Iterations)))
		if i == m.cursor {
			line = rowSel.Render(line)
		} else {
			line = rowNormal.Render(line)
		}
		lb.WriteString(line + "\n")
	}

	var rb strings.Builder
	if m.cursor < len(m.runs) {
		sel := m.runs[m.cursor]
		rb.WriteString(titleStyle.Render(sel.ID) + "\n")
		rb.WriteString(dim.Render(fmt.Sprintf("stop=%s ended=%s", sel.StopReason, sel.EndedAt)) + "\n")
		if sel.Error != "" {
			rb.WriteString(errBadge.Render("error: "+sel.Error) + "\n")
		}
		rb.WriteString("\n")
		for _, ln := range sel.TailLines {
			rb.WriteString(truncate(ln, right) + "\n")
		}
	}

	leftBlock := lipgloss.NewStyle().Width(left).Height(h - 1).Render(lb.String())
	rightBlock := lipgloss.NewStyle().Width(right).Height(h - 1).Render(rb.String())
	return lipgloss.JoinHorizontal(lipgloss.Top, leftBlock, " │ ", rightBlock)
}

func badge(r runInfo) string {
	switch {
	case r.Error != "":
		return errBadge.Render("✗")
	case r.StopReason == "empty":
		return okBadge.Render("✓")
	case r.StopReason == "":
		return runBadge.Render("●")
	default:
		return dim.Render("•")
	}
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func loadRuns(root string) ([]runInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []runInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := loadRun(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		info.ID = e.Name()
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt > out[j].EndedAt })
	return out, nil
}

func loadRun(dir string) (runInfo, error) {
	var r runInfo
	if b, err := os.ReadFile(filepath.Join(dir, "result.json")); err == nil {
		var res struct {
			EndedAt    string `json:"ended_at"`
			Iterations int    `json:"iterations"`
			StopReason string `json:"stop_reason"`
			Error      string `json:"error"`
		}
		_ = json.Unmarshal(b, &res)
		r.EndedAt = res.EndedAt
		r.Iterations = res.Iterations
		r.StopReason = res.StopReason
		r.Error = res.Error
	}
	r.TailLines = tailEvents(filepath.Join(dir, "events.jsonl"), 40)
	return r, nil
}

func tailEvents(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var ev struct {
			At      time.Time `json:"at"`
			Type    string    `json:"type"`
			Subtype string    `json:"subtype"`
		}
		_ = json.Unmarshal(sc.Bytes(), &ev)
		line := fmt.Sprintf("%s %s%s", ev.At.Format("15:04:05"), ev.Type, suffix(ev.Subtype))
		ring = append(ring, line)
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	return ring
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "/" + s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
