package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Crush-inspired centered modal overlay: titled bordered box, filter input,
// filterable list with right-aligned shortcuts, group separators, hint bar.

type modalItem struct {
	label    string
	shortcut string
	group    string // optional separator label rendered above this item
	disabled bool
	action   func(model) (tea.Model, tea.Cmd)
}

type modalState struct {
	title  string
	hint   string
	items  []modalItem
	filter textinput.Model
	cursor int
	open   bool
}

var (
	modalBorder    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("99")).Padding(1, 2)
	modalTitle     = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	modalSlashes   = lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	modalGroup     = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	modalGroupRule = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	modalShortcut  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	modalRowSel    = lipgloss.NewStyle().Background(lipgloss.Color("99")).Foreground(lipgloss.Color("231"))
	modalRow       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	modalRowDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	modalHint      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	modalPrompt    = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	modalDimText   = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

func newModal(title, hint string, items []modalItem) modalState {
	ti := textinput.New()
	ti.Placeholder = "Type to filter"
	ti.Prompt = ""
	ti.CharLimit = 80
	ti.Width = 40
	ti.Focus()
	return modalState{title: title, hint: hint, items: items, filter: ti, open: true}
}

// filtered returns items matching current filter (case-insensitive substring).
func (s modalState) filtered() []modalItem {
	q := strings.ToLower(strings.TrimSpace(s.filter.Value()))
	if q == "" {
		return s.items
	}
	out := make([]modalItem, 0, len(s.items))
	for _, it := range s.items {
		if strings.Contains(strings.ToLower(it.label), q) ||
			strings.Contains(strings.ToLower(it.shortcut), q) {
			out = append(out, it)
		}
	}
	return out
}

func (m model) updateModal(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	items := m.modal.filtered()
	switch km.String() {
	case "esc", "ctrl+c":
		m.modal.open = false
		m.mode = modeList
		return m, nil
	case "down", "ctrl+n":
		if m.modal.cursor < len(items)-1 {
			m.modal.cursor++
		}
		return m, nil
	case "up", "ctrl+p":
		if m.modal.cursor > 0 {
			m.modal.cursor--
		}
		return m, nil
	case "enter":
		if m.modal.cursor >= 0 && m.modal.cursor < len(items) {
			it := items[m.modal.cursor]
			if it.disabled || it.action == nil {
				return m, nil
			}
			m.modal.open = false
			m.mode = modeList
			return it.action(m)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.modal.filter, cmd = m.modal.filter.Update(msg)
	items = m.modal.filtered()
	if m.modal.cursor >= len(items) {
		m.modal.cursor = maxInt(0, len(items)-1)
	}
	return m, cmd
}

// viewModal renders the modal overlay centered on top of the base view.
func (m model) viewModal(base string) string {
	w := m.width
	h := m.height
	if w == 0 {
		w, h = 120, 36
	}

	boxW := w * 3 / 5
	if boxW < 60 {
		boxW = 60
	}
	if boxW > w-4 {
		boxW = w - 4
	}
	innerW := boxW - 6 // padding 2 on each side + 2 for border

	items := m.modal.filtered()

	var b strings.Builder

	titleText := m.modal.title
	slashCount := innerW - lipgloss.Width(titleText) - 1
	if slashCount < 0 {
		slashCount = 0
	}
	b.WriteString(modalTitle.Render(titleText) + " " + modalSlashes.Render(strings.Repeat("/", slashCount)) + "\n\n")

	prompt := modalPrompt.Render("> ")
	b.WriteString(prompt + m.modal.filter.View() + "\n\n")

	if len(items) == 0 {
		b.WriteString(modalDimText.Render("(no matches)") + "\n")
	} else {
		var lastGroup string
		for i, it := range items {
			if it.group != "" && it.group != lastGroup {
				ruleW := innerW - lipgloss.Width(it.group) - 1
				if ruleW < 0 {
					ruleW = 0
				}
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(modalGroup.Render(it.group) + " " + modalGroupRule.Render(strings.Repeat("─", ruleW)) + "\n")
				lastGroup = it.group
			}

			label := it.label
			sc := it.shortcut
			padCount := innerW - lipgloss.Width(label) - lipgloss.Width(sc)
			if padCount < 1 {
				padCount = 1
			}
			line := label + strings.Repeat(" ", padCount) + sc
			if i == m.modal.cursor {
				b.WriteString(modalRowSel.Width(innerW).Render(line) + "\n")
			} else {
				rowStyle := modalRow
				scStyle := modalShortcut
				if it.disabled {
					rowStyle = modalRowDim
					scStyle = modalRowDim
				}
				b.WriteString(rowStyle.Render(label) + strings.Repeat(" ", padCount) + scStyle.Render(sc) + "\n")
			}
		}
	}

	if m.modal.hint != "" {
		b.WriteString("\n" + modalHint.Render(m.modal.hint))
	}

	box := modalBorder.Width(boxW).Render(b.String())
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, box, lipgloss.WithWhitespaceChars(" "))
}

// commandPalette is the default modal: top-level Saturn actions.
func (m model) commandPalette() modalState {
	hasRuns := len(m.runs) > 0 && m.cursor < len(m.runs)
	var sel runInfo
	if hasRuns {
		sel = m.runs[m.cursor]
	}
	awaiting := hasRuns && readRunPhase(m.repoRoot, sel.ID) == "awaiting_approval"

	items := []modalItem{
		{label: "New Task", shortcut: "n", group: "Create",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openNewTask() }},
		{label: "New from GitHub Issue", shortcut: "g",
			action: func(m model) (tea.Model, tea.Cmd) { return m.openNewGH() }},

		{label: "Approve Plan", shortcut: "a", group: "Plan", disabled: !awaiting,
			action: func(m model) (tea.Model, tea.Cmd) { return m.approveSelected() }},
		{label: "View PLAN.md", shortcut: "P", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.viewPlan() }},

		{label: "Open Diff", shortcut: "d", group: "Inspect", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.openDiff() }},
		{label: "All Diffs Summary", shortcut: "D",
			action: func(m model) (tea.Model, tea.Cmd) { return m.enterDiffSummary() }},
		{label: "Open Editor", shortcut: "e", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.openEditor() }},
		{label: "Open Shell", shortcut: "w", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.openShell() }},
		{label: "Attach Claude Session", shortcut: "o", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.openClaude() }},

		{label: "Merge to main", shortcut: "m", group: "Manage", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.mergeRun() }},
		{label: "Kill tmux Session", shortcut: "K", disabled: !hasRuns,
			action: func(m model) (tea.Model, tea.Cmd) { return m.killSession() }},
		{label: "Refresh", shortcut: "r",
			action: func(m model) (tea.Model, tea.Cmd) { return m, refreshCmd(m.root) }},
		{label: "Quit", shortcut: "q",
			action: func(m model) (tea.Model, tea.Cmd) { return m, tea.Quit }},
	}
	return newModal("Commands", "↑/↓ choose · enter confirm · esc cancel · type to filter", items)
}
