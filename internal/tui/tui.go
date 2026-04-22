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
	"strings"
	"time"

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
)

type runInfo struct {
	ID         string
	Iterations int
	StopReason string
	EndedAt    string
	Error      string
	TailLines  []string
	SessionID  string
	Workdir    string
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
	case "d":
		return m.openDiff()
	case "D":
		return m.openDiffSummary()
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
		// Shared-mode task: show last 20 commits with their diffs inline.
		// `git log -p` handles short histories without needing HEAD~N.
		return m, runInPager(m.repoRoot, "git log -p --stat -20")
	}
	// `main..branch` fails if main doesn't exist; fall back to whole branch.
	if !branchExists(m.repoRoot, "main") {
		return m, runInPager(m.repoRoot, fmt.Sprintf("git log -p --stat %s", shellQuote(branch)))
	}
	return m, runInPager(m.repoRoot, fmt.Sprintf("git diff main..%s", shellQuote(branch)))
}

func (m model) openDiffSummary() (tea.Model, tea.Cmd) {
	branches := saturnBranches(m.repoRoot)
	if len(branches) == 0 {
		m.flash = "no saturn/* branches yet"
		return m, nil
	}
	mainExists := branchExists(m.repoRoot, "main")
	var script strings.Builder
	script.WriteString("echo 'saturn agent diffs · q to quit'; echo; ")
	for _, b := range branches {
		if mainExists {
			script.WriteString(fmt.Sprintf("echo '=== %s ==='; git diff --stat main..%s; echo; ",
				b, shellQuote(b)))
		} else {
			script.WriteString(fmt.Sprintf("echo '=== %s ==='; git log --oneline %s | head -20; echo; ",
				b, shellQuote(b)))
		}
	}
	return m, runInPager(m.repoRoot, script.String())
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
		shellCmd := fmt.Sprintf("claude --resume %q", sel.SessionID)
		if err := tmux.NewDetached(name, sel.Workdir, shellCmd); err != nil {
			m.flash = "tmux create failed: " + err.Error()
			return m, nil
		}
	}
	return m, tea.ExecProcess(tmux.AttachCmd(name), func(err error) tea.Msg {
		if err != nil {
			return flashMsg("tmux attach: " + err.Error())
		}
		return flashMsg("detached from " + name + " (still running; K to kill)")
	})
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
	}
	return m.viewList()
}

func (m model) viewList() string {
	if m.err != nil {
		return fmt.Sprintf("error: %v (press q to quit)", m.err)
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
	lb.WriteString(dim.Render(fmt.Sprintf("%d runs · e editor · n quick · g gh · o attach · w shell · d diff · D all-diffs · K kill · r refresh · q quit", len(m.runs))) + "\n")
	if m.flash != "" {
		lb.WriteString(okBadge.Render(m.flash) + "\n")
	}
	lb.WriteString("\n")
	if len(m.runs) == 0 {
		lb.WriteString(dim.Render("no runs yet — press e to write a task in $EDITOR") + "\n")
	}
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
	if len(m.runs) > 0 && m.cursor < len(m.runs) {
		sel := m.runs[m.cursor]
		rb.WriteString(titleStyle.Render(sel.ID) + "\n")
		rb.WriteString(dim.Render(fmt.Sprintf("workdir=%s", sel.Workdir)) + "\n")
		rb.WriteString(dim.Render(fmt.Sprintf("stop=%s ended=%s", sel.StopReason, sel.EndedAt)) + "\n")
		if tmux.SessionExists("saturn-" + sel.ID) {
			rb.WriteString(runBadge.Render("tmux session live — press o to attach") + "\n")
		}
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
		}
		_ = json.Unmarshal(b, &res)
		r.EndedAt = res.EndedAt
		r.Iterations = res.Iterations
		r.StopReason = res.StopReason
		r.Error = res.Error
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
				Type      string `json:"type"`
				Subtype   string `json:"subtype"`
				SessionID string `json:"session_id"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Raw.Type == "system" && ev.Raw.Subtype == "init" && ev.Raw.SessionID != "" {
			latest = ev.Raw.SessionID
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
