package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// readRunPhase returns the trimmed contents of <runDir>/phase, or "" if absent.
// Phases are written by cmd/saturn ("planning", "awaiting_approval",
// "executing", "done") so the TUI can light up the Approve action only when
// the selected run is gated on human approval.
func readRunPhase(repoRoot, taskID string) string {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".saturn", "runs", taskID, "phase"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// openNewTask switches the TUI into the huh-backed new-task form.
func (m model) openNewTask() (tea.Model, tea.Cmd) {
	m.newTask = newTaskForm()
	m.mode = modeNew
	m.flash = ""
	return m, m.newTask.form.Init()
}

// openNewGH switches into the (lightweight) GitHub-issue intake screen.
func (m model) openNewGH() (tea.Model, tea.Cmd) {
	m.ghRef.SetValue("")
	m.ghRef.Focus()
	m.mode = modeGH
	m.flash = ""
	return m, nil
}

// approveSelected runs `saturn approve <id>` for the highlighted run if any
// awaiting_* gate is active (stack, plan, or legacy approval). Mirrors the
// CLI subcommand so behavior stays in one place.
func (m model) approveSelected() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	switch readRunPhase(m.repoRoot, sel.ID) {
	case "awaiting_stack", "awaiting_plan", "awaiting_approval":
		// fall through and approve
	default:
		m.flash = "task " + sel.ID + " is not awaiting approval"
		return m, nil
	}
	exe, err := os.Executable()
	if err != nil {
		exe = "saturn"
	}
	cmd := exec.Command(exe, "approve", sel.ID)
	cmd.Dir = m.repoRoot
	logPath := filepath.Join(m.repoRoot, ".saturn", "tui-spawned.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	if err := cmd.Start(); err != nil {
		m.flash = "approve: " + err.Error()
		return m, nil
	}
	go cmd.Wait()
	m.flash = "approved " + sel.ID + " — execution phase started"
	return m, refreshCmd(m.root)
}

// viewPlan opens PLAN.md for the highlighted run in $PAGER (or less). The
// file lives in the worktree, written by the agent during the planning phase.
func (m model) viewPlan() (tea.Model, tea.Cmd) {
	return m.viewWorkdirFile("PLAN.md")
}

// viewStack opens STACK.md (the architect phase output).
func (m model) viewStack() (tea.Model, tea.Cmd) {
	return m.viewWorkdirFile("STACK.md")
}

// viewWorkdirFile is the shared helper for opening agent-produced markdown
// (PLAN.md, STACK.md, etc.) in $PAGER. Pulled out so the two callers can't
// drift on quoting/error handling.
func (m model) viewWorkdirFile(name string) (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	path := filepath.Join(sel.Workdir, name)
	if _, err := os.Stat(path); err != nil {
		m.flash = "no " + name + " at " + path
		return m, nil
	}
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("%s %s", pager, shellQuote(path)))
	cmd.Dir = sel.Workdir
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return flashMsg("pager: " + err.Error())
		}
		return flashMsg("")
	})
}
