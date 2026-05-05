package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// newTaskState wraps a huh.Form plus the bound values. Storing values in
// a struct (rather than capturing locals) lets us read them back cleanly
// when the form transitions to StateCompleted.
type newTaskState struct {
	form *huh.Form

	title   string
	body    string
	backend string // "" | "claude" | "opencode"
	loop    bool
	plan    bool
	shared  bool
	maxIter string // textinput; parsed on submit (empty = use CLI default)
}

// newTaskForm builds a four-group huh form: basics → behavior → execution →
// confirm. Grouping keeps the screen short on small terminals (huh paginates)
// and matches the way these knobs cluster in the markdown front matter.
func newTaskForm() *newTaskState {
	st := &newTaskState{
		backend: "", // auto
		maxIter: "20",
	}

	titleField := huh.NewInput().
		Key("title").
		Title("Title").
		Description("Short, human-readable. Becomes the H1 in tasks/<id>.md.").
		Placeholder("Fix login redirect").
		Value(&st.title).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("title is required")
			}
			return nil
		})

	bodyField := huh.NewText().
		Key("body").
		Title("Prompt").
		Description("What should the agent do? Use `- [ ]` checklists for multi-step work.").
		Placeholder("Users hitting /login while authenticated should be redirected to /dashboard.").
		Lines(8).
		CharLimit(8000).
		Value(&st.body).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("prompt body is required")
			}
			return nil
		})

	backendField := huh.NewSelect[string]().
		Key("backend").
		Title("Backend").
		Description("Which agent CLI drives this task.").
		Options(
			huh.NewOption("auto (prefer opencode)", ""),
			huh.NewOption("opencode", "opencode"),
			huh.NewOption("claude", "claude"),
		).
		Value(&st.backend)

	loopField := huh.NewConfirm().
		Key("loop").
		Title("Loop until done? (Ralph mode)").
		Description("Off = one shot. On = re-prompt the agent until it reports empty/STOP.").
		Affirmative("Loop").
		Negative("Single-shot").
		Value(&st.loop)

	planField := huh.NewConfirm().
		Key("plan").
		Title("Gate on PLAN.md?").
		Description("Agent writes PLAN.md first; you `a` to approve before execution.").
		Affirmative("Plan first").
		Negative("Skip planning").
		Value(&st.plan)

	sharedField := huh.NewConfirm().
		Key("shared").
		Title("Run in repo root (shared)?").
		Description("Off = isolated worktree at .saturn/wt/<id>/. On = mutates the repo directly. Single-task only.").
		Affirmative("Shared").
		Negative("Worktree").
		Value(&st.shared)

	maxIterField := huh.NewInput().
		Key("max_iter").
		Title("Max iterations").
		Description("Cap for loop mode. Ignored when single-shot. 0 = unlimited.").
		Placeholder("20").
		Value(&st.maxIter).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			n, err := strconv.Atoi(s)
			if err != nil || n < 0 {
				return fmt.Errorf("must be a non-negative integer")
			}
			return nil
		})

	form := huh.NewForm(
		huh.NewGroup(titleField, bodyField).Title("Task"),
		huh.NewGroup(backendField, loopField, planField).Title("Behavior"),
		huh.NewGroup(sharedField, maxIterField).Title("Execution"),
	).
		WithTheme(huh.ThemeCharm()).
		WithShowHelp(true).
		WithShowErrors(true)

	st.form = form
	return st
}

// updateNew routes input into the huh form; on completion, writes the task
// markdown and spawns `saturn run`. On abort (esc / ctrl+c inside huh) we
// return to the list rather than quitting the whole program.
func (m model) updateNew(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.newTask == nil {
		m.mode = modeList
		return m, nil
	}

	// Forward to the form first.
	form, cmd := m.newTask.form.Update(msg)
	if f, ok := form.(*huh.Form); ok {
		m.newTask.form = f
	}

	switch m.newTask.form.State {
	case huh.StateCompleted:
		return m.submitNewTask()
	case huh.StateAborted:
		m.mode = modeList
		m.newTask = nil
		m.flash = "cancelled"
		return m, nil
	}

	return m, cmd
}

// viewNew renders the huh form inside our standard rounded box so it visually
// matches the rest of the TUI. We size the box to the form's preferred width
// and leave it un-centered so the keys at the bottom stay readable.
func (m model) viewNew() string {
	if m.newTask == nil {
		return ""
	}
	w := m.width
	if w == 0 {
		w = 100
	}
	boxW := w - 4
	if boxW > 100 {
		boxW = 100
	}
	if boxW < 60 {
		boxW = 60
	}

	header := titleStyle.Render("▎ new task") + "  " +
		dim.Render("tab next · shift+tab prev · enter confirm · esc cancel")

	body := m.newTask.form.WithWidth(boxW - 4).View()
	if m.flash != "" {
		body += "\n" + errBadge.Render(m.flash)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("99")).
		Padding(1, 2).
		Width(boxW).
		Render(header + "\n\n" + body)

	return box
}

var newTaskSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// taskSlug mirrors internal/task.slugify so the id we write into front matter
// matches what task.ParseFile will derive when re-reading the file. We can't
// import task.slugify directly (unexported); keep the two implementations
// identical or things like .saturn/wt/<id>/ will diverge from <id>.md.
func taskSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = newTaskSlugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = fmt.Sprintf("task-%d", time.Now().Unix())
	}
	return s
}

func (m model) submitNewTask() (tea.Model, tea.Cmd) {
	st := m.newTask
	title := strings.TrimSpace(st.title)
	body := strings.TrimSpace(st.body)
	if title == "" || body == "" {
		// huh's validators should prevent this, but guard anyway.
		m.flash = "title and body required"
		return m, nil
	}

	id := taskSlug(title)

	// Compose the markdown with all the front-matter knobs the form gathered.
	// Mirror cmd/saturn's expected schema: keys are written only when their
	// value differs from the default the parser assumes (so files stay tidy).
	var fm strings.Builder
	fm.WriteString("---\n")
	fmt.Fprintf(&fm, "id: %s\n", id)
	fmt.Fprintf(&fm, "title: %s\n", title)
	if st.backend != "" {
		fmt.Fprintf(&fm, "backend: %s\n", st.backend)
	}
	if st.loop {
		fm.WriteString("loop: true\n")
	}
	if st.plan {
		fm.WriteString("plan: true\n")
	}
	if st.shared {
		fm.WriteString("shared: true\n")
	}
	fm.WriteString("---\n")

	content := fmt.Sprintf("%s# %s\n\n%s\n", fm.String(), title, body)
	path := filepath.Join(m.repoRoot, "tasks", id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.flash = "mkdir: " + err.Error()
		return m, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		m.flash = "write: " + err.Error()
		return m, nil
	}

	// max-iter is per-run (CLI flag), not per-task, so pass it through here.
	args := []string{"run"}
	if mi := strings.TrimSpace(st.maxIter); mi != "" && mi != "20" {
		args = append(args, "--max-iter", mi)
	}
	args = append(args, path)
	spawnSaturn(m.repoRoot, args...)

	m.mode = modeList
	m.newTask = nil
	m.flash = "launched " + id
	return m, refreshCmd(m.root)
}
