package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrewn6/saturn/internal/agent"
	"github.com/andrewn6/saturn/internal/gitops"
	"github.com/andrewn6/saturn/internal/task"
	"github.com/andrewn6/saturn/internal/tmux"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type mode int

const (
	modeList mode = iota
	modeNew
	modeGH
	modeDiffSummary
	modeDiffView
)

type diffEntry struct {
	Branch  string
	Files   []numstatRow
	Added   int
	Deleted int
	NoMain  bool
}

type runInfo struct {
	ID         string
	Iterations int
	StopReason string
	EndedAt    string
	Error      string
	TailLines  []string
	SessionID  string
	Workdir    string
	Backend    string
}

type model struct {
	root     string
	repoRoot string
	runs     []runInfo
	cursor   int
	width    int
	height   int
	err      error

	mode   mode
	title  textinput.Model
	shared bool
	body   textarea.Model
	ghRef  textinput.Model
	focus  int
	flash  string

	diffEntries []diffEntry
	diffCursor  int

	diff diffViewState
}

type tickMsg time.Time
type refreshMsg struct {
	runs []runInfo
	err  error
}
type flashMsg string

func Run(runsRoot string) error {
	repoRoot := filepath.Dir(filepath.Dir(runsRoot))
	ti := textinput.New()
	ti.Placeholder = "short task title"
	ti.CharLimit = 120
	ti.Width = 60

	ta := textarea.New()
	ta.Placeholder = "what should the agent do? use `- [ ]` checklist lines."
	ta.SetWidth(80)
	ta.SetHeight(10)

	gh := textinput.New()
	gh.Placeholder = "owner/repo#123"
	gh.CharLimit = 120
	gh.Width = 60

	m := model{root: runsRoot, repoRoot: repoRoot, title: ti, body: ta, ghRef: gh}
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(refreshCmd(m.root), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(1*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func refreshCmd(root string) tea.Cmd {
	return func() tea.Msg {
		runs, err := loadRuns(root)
		return refreshMsg{runs: runs, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(refreshCmd(m.root), tickCmd())
	case refreshMsg:
		m.err = msg.err
		m.runs = msg.runs
		if m.cursor >= len(m.runs) {
			m.cursor = maxInt(0, len(m.runs)-1)
		}
		return m, nil
	case flashMsg:
		m.flash = string(msg)
		return m, nil
	}

	switch m.mode {
	case modeList:
		return m.updateList(msg)
	case modeNew:
		return m.updateNew(msg)
	case modeGH:
		return m.updateGH(msg)
	case modeDiffSummary:
		return m.updateDiffSummary(msg)
	case modeDiffView:
		return m.updateDiffView(msg)
	}
	return m, nil
}

func (m model) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
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
	case "n":
		m.mode = modeNew
		m.focus = 0
		m.title.Focus()
		m.body.Blur()
		m.flash = ""
		return m, textinput.Blink
	case "g":
		m.mode = modeGH
		m.ghRef.Focus()
		m.flash = ""
		return m, textinput.Blink
	case "o":
		return m.openClaude()
	case "w":
		return m.openShell()
	case "e":
		return m.openEditor()
	case "K":
		return m.killSession()
	case "m":
		return m.mergeRun()
	case "d":
		return m.openDiff()
	case "D":
		return m.enterDiffSummary()
	}
	return m, nil
}

func (m model) openDiff() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	branch := "saturn/" + sel.ID
	if !branchExists(m.repoRoot, branch) {
		m.flash = "no branch saturn/" + sel.ID + " (shared mode? press w to shell in)"
		return m, nil
	}
	return m.enterDiffView(branch, modeList)
}

// styledHeader emits a shell `printf` that prints a magenta bar + agent name
// + stats. Works in any ANSI-respecting pager (less -R).
func styledHeader(agent, stats string) string {
	line := fmt.Sprintf("\x1b[1;35m▎ %s\x1b[0m  \x1b[0;90m%s\x1b[0m\n\n", agent, stats)
	return "printf '%b' " + shellQuote(line)
}

// diffCmdFor prefers `delta` when available; otherwise forces git colors.
func diffCmdFor(base, branch string) string {
	if _, err := exec.LookPath("delta"); err == nil {
		return fmt.Sprintf("git diff %s..%s | delta", shellQuote(base), shellQuote(branch))
	}
	return fmt.Sprintf("git -c color.ui=always diff %s..%s", shellQuote(base), shellQuote(branch))
}

func diffCmdForBranchOnly(branch string) string {
	if _, err := exec.LookPath("delta"); err == nil {
		return fmt.Sprintf("git log -p %s | delta", shellQuote(branch))
	}
	return fmt.Sprintf("git -c color.ui=always log -p --stat %s", shellQuote(branch))
}

func shortStat(repoRoot, base, branch string) string {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--shortstat", base+".."+branch)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (m model) enterDiffSummary() (tea.Model, tea.Cmd) {
	branches := saturnBranches(m.repoRoot)
	if len(branches) == 0 {
		m.flash = "no saturn/* branches yet"
		return m, nil
	}
	mainExists := branchExists(m.repoRoot, "main")
	entries := make([]diffEntry, 0, len(branches))
	for _, br := range branches {
		e := diffEntry{Branch: br, NoMain: !mainExists}
		if mainExists {
			files, add, del := numstat(m.repoRoot, "main", br)
			e.Files = files
			e.Added = add
			e.Deleted = del
		}
		entries = append(entries, e)
	}
	m.diffEntries = entries
	m.diffCursor = 0
	m.mode = modeDiffSummary
	m.flash = ""
	return m, nil
}

func (m model) updateDiffSummary(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "q", "esc":
		m.mode = modeList
		return m, nil
	case "j", "down":
		if m.diffCursor < len(m.diffEntries)-1 {
			m.diffCursor++
		}
	case "k", "up":
		if m.diffCursor > 0 {
			m.diffCursor--
		}
	case "r":
		return m.enterDiffSummary()
	case "enter":
		if len(m.diffEntries) == 0 {
			return m, nil
		}
		entry := m.diffEntries[m.diffCursor]
		if entry.NoMain {
			m.flash = "no main branch — diff view unavailable"
			return m, nil
		}
		return m.enterDiffView(entry.Branch, modeDiffSummary)
	}
	return m, nil
}

// buildDiffSummary renders an ANSI-colored per-agent diff table:
//
//	▎ saturn/login-fix                    3 files  +24 -3
//	  src/auth.py                                  +24 -3
//	  src/tests/auth_test.py                       +10 -0
//
// Colors: magenta agent names, green adds, red dels, dim file paths.
func buildDiffSummary(repoRoot string, branches []string, mainExists bool) string {
	var b strings.Builder
	b.WriteString("\x1b[1msaturn — all agent diffs\x1b[0m  \x1b[0;90m(q to quit)\x1b[0m\n\n")

	for _, br := range branches {
		if !mainExists {
			b.WriteString(fmt.Sprintf("\x1b[1;35m▎ %s\x1b[0m  \x1b[0;90m(no main — see log)\x1b[0m\n\n", br))
			continue
		}
		rows, totalAdd, totalDel := numstat(repoRoot, "main", br)
		fileCount := len(rows)
		b.WriteString(fmt.Sprintf("\x1b[1;35m▎ %s\x1b[0m  \x1b[0;90m%d file%s\x1b[0m  \x1b[32m+%d\x1b[0m \x1b[31m-%d\x1b[0m\n",
			br, fileCount, plural(fileCount), totalAdd, totalDel))
		if fileCount == 0 {
			b.WriteString("  \x1b[0;90m(no changes yet)\x1b[0m\n\n")
			continue
		}
		for _, r := range rows {
			b.WriteString(fmt.Sprintf("  \x1b[0;37m%-50s\x1b[0m  \x1b[32m+%-4d\x1b[0m \x1b[31m-%d\x1b[0m\n",
				truncate(r.Path, 50), r.Added, r.Deleted))
		}
		b.WriteString("\n")
	}
	return b.String()
}

type numstatRow struct {
	Added, Deleted int
	Path           string
}

func numstat(repoRoot, base, branch string) (rows []numstatRow, totalAdd, totalDel int) {
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--numstat", base+".."+branch)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, 0
	}
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		a, _ := strconv.Atoi(parts[0])
		d, _ := strconv.Atoi(parts[1])
		rows = append(rows, numstatRow{Added: a, Deleted: d, Path: parts[2]})
		totalAdd += a
		totalDel += d
	}
	return rows, totalAdd, totalDel
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func branchExists(repoRoot, name string) bool {
	cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	return cmd.Run() == nil
}

func saturnBranches(repoRoot string) []string {
	cmd := exec.Command("git", "-C", repoRoot, "for-each-ref",
		"--format=%(refname:short)", "refs/heads/saturn/")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var bs []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		ln = strings.TrimSpace(ln)
		if ln != "" {
			bs = append(bs, ln)
		}
	}
	return bs
}

func runInPager(workdir, shellScript string) tea.Cmd {
	pager := os.Getenv("PAGER")
	if pager == "" {
		pager = "less -R"
	}
	full := fmt.Sprintf("%s | %s", shellScript, pager)
	cmd := exec.Command("sh", "-c", full)
	cmd.Dir = workdir
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return flashMsg("pager: " + err.Error())
		}
		return flashMsg("")
	})
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m model) openClaude() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	if sel.SessionID == "" {
		m.flash = "no session id yet (first iteration not complete)"
		return m, nil
	}
	if !tmux.Available() {
		m.flash = "tmux not installed — install tmux and retry"
		return m, nil
	}
	name := "saturn-" + sel.ID
	if !tmux.SessionExists(name) {
		// Shell-quote the session id so special chars survive.
		attach := agent.AttachCmd(sel.Backend, sel.SessionID)
		shellCmd := strings.Join(append([]string{attach.Path}, attach.Args[1:]...), " ")
		if err := tmux.NewDetached(name, sel.Workdir, shellCmd); err != nil {
			m.flash = "tmux create failed: " + err.Error()
			return m, nil
		}
	}
	return m, tea.ExecProcess(tmux.AttachCmd(name), func(err error) tea.Msg {
		if err != nil {
			return flashMsg("tmux attach: " + err.Error())
		}
		return flashMsg("detached " + name + " — Ctrl+\\ detach · Ctrl+] kill (still running; K here to kill)")
	})
}

func (m model) mergeRun() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	branch := "saturn/" + sel.ID
	base := "main"
	if !branchExistsCached(m.repoRoot, branch) {
		m.flash = "no branch " + branch + " (shared mode? nothing to merge)"
		return m, nil
	}
	if !branchExistsCached(m.repoRoot, base) {
		m.flash = "no main branch — set base manually via CLI"
		return m, nil
	}
	conflicts, err := gitops.Conflicts(m.repoRoot, base, branch)
	if err != nil {
		m.flash = "preflight: " + err.Error()
		return m, nil
	}
	if len(conflicts) > 0 {
		summary := strings.Join(conflicts, ", ")
		if len(summary) > 80 {
			summary = summary[:80] + "…"
		}
		m.flash = fmt.Sprintf("conflicts in %d file(s): %s — press w to shell in and resolve",
			len(conflicts), summary)
		return m, nil
	}
	if err := gitops.Merge(m.repoRoot, base, branch); err != nil {
		m.flash = "merge failed: " + err.Error()
		return m, nil
	}
	if err := gitops.Cleanup(m.repoRoot, sel.ID); err != nil {
		m.flash = "merged but cleanup failed: " + err.Error()
		return m, refreshCmd(m.root)
	}
	m.flash = "merged " + branch + " into " + base + " and cleaned up"
	return m, refreshCmd(m.root)
}

func (m model) killSession() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	name := "saturn-" + sel.ID
	if !tmux.SessionExists(name) {
		m.flash = "no tmux session for " + sel.ID
		return m, nil
	}
	if err := tmux.KillSession(name); err != nil {
		m.flash = "kill failed: " + err.Error()
		return m, nil
	}
	m.flash = "killed " + name
	return m, nil
}

func (m model) openShell() (tea.Model, tea.Cmd) {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return m, nil
	}
	sel := m.runs[m.cursor]
	if sel.Workdir == "" {
		m.flash = "no workdir for this run"
		return m, nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = sel.Workdir
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return flashMsg("shell exited: " + err.Error())
		}
		return flashMsg("back from " + sel.Workdir)
	})
}

func (m model) updateNew(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, isKey := msg.(tea.KeyMsg)
	if isKey {
		switch km.String() {
		case "esc":
			m.mode = modeList
			m.title.Blur()
			m.body.Blur()
			return m, nil
		case "tab":
			m.focus = (m.focus + 1) % 3
			m.applyFocus()
			return m, nil
		case "shift+tab":
			m.focus = (m.focus + 2) % 3
			m.applyFocus()
			return m, nil
		case "ctrl+s":
			return m.submitNew()
		case " ":
			if m.focus == 1 {
				m.shared = !m.shared
				return m, nil
			}
		}
	}
	var cmds []tea.Cmd
	switch m.focus {
	case 0:
		var c tea.Cmd
		m.title, c = m.title.Update(msg)
		cmds = append(cmds, c)
	case 2:
		var c tea.Cmd
		m.body, c = m.body.Update(msg)
		cmds = append(cmds, c)
	}
	return m, tea.Batch(cmds...)
}

func (m model) updateGH(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.mode = modeList
			m.ghRef.Blur()
			return m, nil
		case "enter":
			return m.submitGH()
		}
	}
	var c tea.Cmd
	m.ghRef, c = m.ghRef.Update(msg)
	return m, c
}

func (m *model) applyFocus() {
	m.title.Blur()
	m.body.Blur()
	switch m.focus {
	case 0:
		m.title.Focus()
	case 2:
		m.body.Focus()
	}
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = fmt.Sprintf("task-%d", time.Now().Unix())
	}
	return s
}

func (m model) submitNew() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.title.Value())
	body := strings.TrimSpace(m.body.Value())
	if title == "" || body == "" {
		m.flash = "title and body required"
		return m, nil
	}
	id := slugify(title)
	content := fmt.Sprintf("---\nid: %s\nshared: %t\n---\n# %s\n%s\n", id, m.shared, title, body)
	path := filepath.Join(m.repoRoot, "tasks", id+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		m.flash = "mkdir: " + err.Error()
		return m, nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		m.flash = "write: " + err.Error()
		return m, nil
	}
	spawnRun(m.repoRoot, path)
	m.mode = modeList
	m.title.SetValue("")
	m.body.SetValue("")
	m.shared = false
	m.title.Blur()
	m.body.Blur()
	m.flash = "launched " + id
	return m, refreshCmd(m.root)
}

func (m model) submitGH() (tea.Model, tea.Cmd) {
	ref := strings.TrimSpace(m.ghRef.Value())
	if ref == "" {
		m.flash = "paste an owner/repo#N ref"
		return m, nil
	}
	spawnRunArg(m.repoRoot, ref)
	m.mode = modeList
	m.ghRef.SetValue("")
	m.ghRef.Blur()
	m.flash = "launched " + ref
	return m, refreshCmd(m.root)
}

func spawnRun(repoRoot, taskPath string) { spawnRunArg(repoRoot, taskPath) }

func spawnRunArg(repoRoot, arg string) {
	exe, err := os.Executable()
	if err != nil {
		exe = "saturn"
	}
	cmd := exec.Command(exe, "run", arg)
	cmd.Dir = repoRoot
	logPath := filepath.Join(repoRoot, ".saturn", "tui-spawned.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	if lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		cmd.Stdout = lf
		cmd.Stderr = lf
	}
	_ = cmd.Start()
	go cmd.Wait()
}

func (m model) openEditor() (tea.Model, tea.Cmd) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	tmp, err := os.CreateTemp("", "saturn-task-*.md")
	if err != nil {
		m.flash = "tempfile: " + err.Error()
		return m, nil
	}
	tmpPath := tmp.Name()
	_, _ = tmp.WriteString(taskTemplate)
	_ = tmp.Close()

	cmd := exec.Command(editor, tmpPath)
	repoRoot := m.repoRoot
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(tmpPath)
		if err != nil {
			return flashMsg("editor exited: " + err.Error())
		}
		contents, rerr := os.ReadFile(tmpPath)
		if rerr != nil {
			return flashMsg("read: " + rerr.Error())
		}
		if strings.TrimSpace(string(contents)) == strings.TrimSpace(taskTemplate) {
			return flashMsg("no changes — task not launched")
		}
		t, perr := task.ParseFile(tmpPath)
		if perr != nil {
			return flashMsg("parse: " + perr.Error())
		}
		dest := filepath.Join(repoRoot, "tasks", t.ID+".md")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return flashMsg("mkdir: " + err.Error())
		}
		if err := os.WriteFile(dest, contents, 0o644); err != nil {
			return flashMsg("write: " + err.Error())
		}
		spawnRunArg(repoRoot, dest)
		return flashMsg("launched " + t.ID)
	})
}

const taskTemplate = `---
id: new-task
shared: false
---
# Replace with a title

Describe the task here. Either write a free-form prompt, or list work as:

- [ ] step one
- [ ] step two
`

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	rowSel     = lipgloss.NewStyle().Background(lipgloss.Color("238")).Foreground(lipgloss.Color("230"))
	rowNormal  = lipgloss.NewStyle()
	dim        = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	okBadge    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	runBadge   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
	boxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
)

func (m model) View() string {
	switch m.mode {
	case modeNew:
		return m.viewNew()
	case modeGH:
		return m.viewGH()
	case modeDiffSummary:
		return m.viewDiffSummary()
	case modeDiffView:
		return m.viewDiffView()
	}
	return m.viewList()
}

func (m model) viewList() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v (press q to quit)", m.err)
	}
	return m.viewListNew()
}

func (m model) viewNew() string {
	sharedLabel := "[ ] shared worktree"
	if m.shared {
		sharedLabel = "[x] shared worktree"
	}
	sharedStyled := sharedLabel
	if m.focus == 1 {
		sharedStyled = rowSel.Render(sharedLabel)
	}
	var b strings.Builder
	b.WriteString(titleStyle.Render("new task") + "\n\n")
	b.WriteString(labelStyle.Render("title") + "\n" + m.title.View() + "\n\n")
	b.WriteString(labelStyle.Render("shared") + "  " + sharedStyled + dim.Render("  (space to toggle)") + "\n\n")
	b.WriteString(labelStyle.Render("body") + "\n" + m.body.View() + "\n\n")
	if m.flash != "" {
		b.WriteString(errBadge.Render(m.flash) + "\n")
	}
	b.WriteString(dim.Render("tab next · shift+tab prev · ctrl+s submit · esc cancel"))
	return boxStyle.Render(b.String())
}

func (m model) viewGH() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("launch from github issue") + "\n\n")
	b.WriteString(labelStyle.Render("ref") + "\n" + m.ghRef.View() + "\n\n")
	if m.flash != "" {
		b.WriteString(errBadge.Render(m.flash) + "\n")
	}
	b.WriteString(dim.Render("enter submit · esc cancel"))
	return boxStyle.Render(b.String())
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
		repoRoot := filepath.Dir(filepath.Dir(root))
		wt := filepath.Join(repoRoot, ".saturn", "wt", e.Name())
		if st, err := os.Stat(wt); err == nil && st.IsDir() {
			info.Workdir = wt
		} else {
			info.Workdir = repoRoot
		}
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
			Backend    string `json:"backend"`
		}
		_ = json.Unmarshal(b, &res)
		r.EndedAt = res.EndedAt
		r.Iterations = res.Iterations
		r.StopReason = res.StopReason
		r.Error = res.Error
		r.Backend = res.Backend
	}
	r.TailLines = tailEvents(filepath.Join(dir, "events.jsonl"), 40)
	r.SessionID = latestSessionID(filepath.Join(dir, "events.jsonl"))
	return r, nil
}

// latestSessionID walks the events.jsonl and returns the session_id from the
// most recent system/init event (each iteration typically starts a new one).
func latestSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var latest string
	for sc.Scan() {
		var ev struct {
			Raw struct {
				Type        string `json:"type"`
				Subtype     string `json:"subtype"`
				SessionID   string `json:"session_id"`
				SessionIDOC string `json:"sessionID"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Raw.Type == "system" && ev.Raw.Subtype == "init" && ev.Raw.SessionID != "" {
			latest = ev.Raw.SessionID
		}
		// Opencode: every event carries sessionID; capture from step_start.
		if ev.Raw.Type == "step_start" && ev.Raw.SessionIDOC != "" {
			latest = ev.Raw.SessionIDOC
		}
	}
	return latest
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
		for _, ln := range summarizeEvent(sc.Bytes()) {
			ring = append(ring, ln)
			if len(ring) > n {
				ring = ring[1:]
			}
		}
	}
	return ring
}

// summarizeEvent turns one stream-json line into 0..N readable summary lines.
func summarizeEvent(raw []byte) []string {
	var ev struct {
		At  time.Time       `json:"at"`
		Raw json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	var inner struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Role    string            `json:"role"`
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
		Result     string  `json:"result"`
		DurationMs int64   `json:"duration_ms"`
		TotalCost  float64 `json:"total_cost_usd"`
		IsError    bool    `json:"is_error"`
	}
	_ = json.Unmarshal(ev.Raw, &inner)

	ts := ev.At.Format("15:04:05")
	// Opencode event shapes (step_start / text / step_finish / tool).
	switch inner.Type {
	case "step_start":
		return []string{ts + " ● session started"}
	case "text":
		// opencode text part
		var part struct {
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		_ = json.Unmarshal(ev.Raw, &part)
		t := strings.TrimSpace(part.Part.Text)
		if t == "" {
			return nil
		}
		if i := strings.Index(t, "\n"); i > 0 {
			t = t[:i]
		}
		if len(t) > 80 {
			t = t[:80] + "…"
		}
		return []string{ts + " ▸ " + t}
	case "tool":
		var part struct {
			Part struct {
				Name  string                 `json:"name"`
				Input map[string]interface{} `json:"input"`
			} `json:"part"`
		}
		_ = json.Unmarshal(ev.Raw, &part)
		return []string{ts + " → " + part.Part.Name + "(" + summarizeToolArg(part.Part.Name, part.Part.Input) + ")"}
	case "step_finish":
		var part struct {
			Part struct {
				Cost float64 `json:"cost"`
			} `json:"part"`
		}
		_ = json.Unmarshal(ev.Raw, &part)
		return []string{fmt.Sprintf("%s ✓ done · $%.4f", ts, part.Part.Cost)}
	}
	switch inner.Type {
	case "system":
		if inner.Subtype == "init" {
			return []string{ts + " ● session started"}
		}
		return nil
	case "assistant":
		var out []string
		for _, c := range inner.Message.Content {
			if line := summarizeContent(ts, c); line != "" {
				out = append(out, line)
			}
		}
		return out
	case "user":
		return nil
	case "result":
		dur := time.Duration(inner.DurationMs) * time.Millisecond
		mark := "✓"
		if inner.IsError {
			mark = "✗"
		}
		return []string{fmt.Sprintf("%s %s done · %s · $%.4f",
			ts, mark, formatDuration(dur.Truncate(time.Second)), inner.TotalCost)}
	}
	return nil
}

func summarizeContent(ts string, raw json.RawMessage) string {
	var c struct {
		Type  string                 `json:"type"`
		Text  string                 `json:"text"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return ""
	}
	switch c.Type {
	case "text":
		t := strings.TrimSpace(c.Text)
		if t == "" {
			return ""
		}
		if i := strings.Index(t, "\n"); i > 0 {
			t = t[:i]
		}
		if len(t) > 80 {
			t = t[:80] + "…"
		}
		return ts + " ▸ " + t
	case "tool_use":
		return ts + " → " + c.Name + "(" + summarizeToolArg(c.Name, c.Input) + ")"
	}
	return ""
}

func summarizeToolArg(name string, in map[string]interface{}) string {
	if in == nil {
		return ""
	}
	pick := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := in[k]; ok {
				if s, ok := v.(string); ok {
					if len(s) > 60 {
						return s[:60] + "…"
					}
					return s
				}
			}
		}
		return ""
	}
	switch name {
	case "Bash":
		return pick("command")
	case "Read", "Edit", "Write", "MultiEdit", "NotebookEdit":
		return pick("file_path", "path")
	case "Grep", "Glob":
		return pick("pattern")
	case "Task":
		return pick("description", "subagent_type")
	case "WebFetch", "WebSearch":
		return pick("url", "query")
	default:
		return pick("file_path", "path", "command", "pattern", "query", "url")
	}
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
