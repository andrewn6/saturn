package tui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrewn6/saturn/internal/tmux"
	"github.com/charmbracelet/lipgloss"
)

// Dashboard layout: top bar (title + global stats + sparkline),
// list pane (left), detail pane (right), bottom footer with contextual keys.

var (
	headerBar = lipgloss.NewStyle().
			Foreground(lipgloss.Color("231")).
			Background(lipgloss.Color("57")).
			Padding(0, 1).
			Bold(true)
	headerStat = lipgloss.NewStyle().Foreground(lipgloss.Color("249"))
	footerBar  = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Padding(0, 1)
	listBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	detailBox  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	sectionHdr = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
	statKey    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	statVal    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	liveDot    = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render("●")
	doneDot    = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("✓")
	errDot     = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("✗")
	costStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	sparkColor = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
)

func (m model) viewListNew() string {
	w, h := m.width, m.height
	if w == 0 {
		w, h = 120, 36
	}

	live, totalCost := globalStats(m.runs, m.repoRoot)
	hourly := hourlyCosts(m.repoRoot, m.runs, 24)
	header := fmt.Sprintf("saturn  %s  %s  %s  %s %s",
		headerStat.Render(fmt.Sprintf("%d runs", len(m.runs))),
		headerStat.Render(fmt.Sprintf("%d live", live)),
		costStyle.Render(fmt.Sprintf("$%.2f total", totalCost)),
		sparkColor.Render(sparkline(hourly)),
		headerStat.Render("/24h"))
	headerLine := headerBar.Width(w).Render(header)

	leftW := w / 3
	if leftW < 36 {
		leftW = 36
	}
	rightW := w - leftW - 2
	if rightW < 40 {
		rightW = 40
	}
	bodyH := h - 3

	listPane := listBox.Width(leftW - 2).Height(bodyH).Render(m.renderListPane(bodyH - 2))
	detailPane := detailBox.Width(rightW - 2).Height(bodyH).Render(m.renderDetailPane(rightW - 4))

	body := lipgloss.JoinHorizontal(lipgloss.Top, listPane, detailPane)

	keys := "e new · o attach · d diff · D all-diffs · w shell · K kill · r refresh · q quit"
	if m.flash != "" {
		keys = m.flash + "  ·  " + keys
	}
	footer := footerBar.Width(w).Render(keys)

	return headerLine + "\n" + body + "\n" + footer
}

func (m model) renderListPane(height int) string {
	var b strings.Builder
	b.WriteString(sectionHdr.Render("Runs") + "\n\n")
	if len(m.runs) == 0 {
		b.WriteString(dim.Render("no runs yet") + "\n")
		b.WriteString(dim.Render("press e to write a task") + "\n")
		return b.String()
	}
	for i, r := range m.runs {
		dot := badgeDot(r)
		title := truncate(r.ID, 22)
		elapsed := runElapsed(m.repoRoot, r)
		tmuxMark := ""
		if tmux.SessionExists("saturn-" + r.ID) {
			tmuxMark = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Render(" ⌬")
		}
		sub := fmt.Sprintf("%d iter%s · %s", r.Iterations, plural(r.Iterations), elapsed)

		if i == m.cursor {
			rowStyle := lipgloss.NewStyle().Background(lipgloss.Color("237")).Foreground(lipgloss.Color("231")).Width(36).Padding(0, 1)
			b.WriteString(rowStyle.Render(dot+" "+title+tmuxMark) + "\n")
			b.WriteString(rowStyle.Render("  "+dim.Render(sub)) + "\n")
		} else {
			b.WriteString(dot + " " + statVal.Render(title) + tmuxMark + "\n")
			b.WriteString("  " + dim.Render(sub) + "\n")
		}
	}
	return b.String()
}

func (m model) renderDetailPane(width int) string {
	if len(m.runs) == 0 || m.cursor >= len(m.runs) {
		return dim.Render("(no runs to inspect)")
	}
	r := m.runs[m.cursor]
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")).Render("▎ " + r.ID)
	b.WriteString(title + "  " + statusLabel(r) + "\n\n")

	stats := [][2]string{
		{"workdir", truncate(r.Workdir, width-12)},
		{"branch", "saturn/" + r.ID},
		{"iters", fmt.Sprintf("%d", r.Iterations)},
		{"elapsed", runElapsed(m.repoRoot, r)},
		{"cost", "$" + fmt.Sprintf("%.4f", runCost(m.repoRoot, r))},
	}
	for _, kv := range stats {
		b.WriteString(statKey.Render(fmt.Sprintf("  %-9s", kv[0])) + statVal.Render(kv[1]) + "\n")
	}

	if r.Error != "" {
		b.WriteString("\n" + errDot + " " + errBadge.Render(r.Error) + "\n")
	}

	b.WriteString("\n" + sectionHdr.Render("Live events") + "\n")
	tailN := len(r.TailLines)
	if tailN > 10 {
		r.TailLines = r.TailLines[tailN-10:]
	}
	for _, ln := range r.TailLines {
		b.WriteString("  " + dim.Render(truncate(ln, width-2)) + "\n")
	}
	if len(r.TailLines) == 0 {
		b.WriteString("  " + dim.Render("(no events yet)") + "\n")
	}

	b.WriteString("\n" + sectionHdr.Render("Files changed") + "\n")
	files := runFilesChanged(m.repoRoot, r)
	if len(files) == 0 {
		b.WriteString("  " + dim.Render("(no committed changes yet)") + "\n")
	} else {
		shown := files
		if len(shown) > 6 {
			shown = shown[:6]
		}
		for _, f := range shown {
			b.WriteString(fmt.Sprintf("  %s  %s %s\n",
				pathStyle.Render(truncate(f.Path, width-18)),
				addCntStyle.Render(fmt.Sprintf("+%d", f.Added)),
				delCntStyle.Render(fmt.Sprintf("-%d", f.Deleted))))
		}
		if len(files) > 6 {
			b.WriteString("  " + dim.Render(fmt.Sprintf("…%d more (press d to view)", len(files)-6)) + "\n")
		}
	}
	return b.String()
}

func badgeDot(r runInfo) string {
	switch {
	case r.Error != "":
		return errDot
	case r.StopReason == "empty":
		return doneDot
	case r.StopReason == "":
		return liveDot
	default:
		return doneDot
	}
}

func statusLabel(r runInfo) string {
	switch {
	case r.Error != "":
		return errBadge.Render("error")
	case r.StopReason == "empty":
		return okBadge.Render("done")
	case r.StopReason == "":
		return runBadge.Render("running")
	default:
		return okBadge.Render(string(r.StopReason))
	}
}

func runElapsed(repoRoot string, r runInfo) string {
	startedAt, endedAt := readIterTimes(filepath.Join(repoRoot, ".saturn", "runs", r.ID, "iterations.jsonl"))
	if startedAt.IsZero() {
		return "—"
	}
	end := endedAt
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(startedAt).Truncate(time.Second)
	return formatDuration(d)
}

func readIterTimes(path string) (started, ended time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var rec struct {
			StartedAt time.Time `json:"started_at"`
			EndedAt   time.Time `json:"ended_at"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if started.IsZero() && !rec.StartedAt.IsZero() {
			started = rec.StartedAt
		}
		if !rec.EndedAt.IsZero() {
			ended = rec.EndedAt
		}
	}
	return
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func runCost(repoRoot string, r runInfo) float64 {
	path := filepath.Join(repoRoot, ".saturn", "runs", r.ID, "events.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	total := 0.0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		var ev struct {
			Raw struct {
				Type string  `json:"type"`
				Cost float64 `json:"total_cost_usd"`
			} `json:"raw"`
		}
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Raw.Type == "result" && ev.Raw.Cost > 0 {
			total += ev.Raw.Cost
		}
	}
	return total
}

func runFilesChanged(repoRoot string, r runInfo) []numstatRow {
	branch := "saturn/" + r.ID
	if !branchExistsCached(repoRoot, branch) {
		return nil
	}
	if !branchExistsCached(repoRoot, "main") {
		return nil
	}
	cmd := exec.Command("git", "-C", repoRoot, "diff", "--numstat", "main.."+branch)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var rows []numstatRow
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		var a, d int
		fmt.Sscanf(parts[0], "%d", &a)
		fmt.Sscanf(parts[1], "%d", &d)
		rows = append(rows, numstatRow{Added: a, Deleted: d, Path: parts[2]})
	}
	return rows
}

var (
	branchCache  = map[string]bool{}
	branchCacheT time.Time
)

func branchExistsCached(repoRoot, name string) bool {
	if time.Since(branchCacheT) > 2*time.Second {
		branchCache = map[string]bool{}
		branchCacheT = time.Now()
	}
	key := repoRoot + "|" + name
	if v, ok := branchCache[key]; ok {
		return v
	}
	cmd := exec.Command("git", "-C", repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+name)
	v := cmd.Run() == nil
	branchCache[key] = v
	return v
}

func globalStats(runs []runInfo, repoRoot string) (live int, totalCost float64) {
	for _, r := range runs {
		if r.StopReason == "" && r.Error == "" {
			live++
		}
		totalCost += runCost(repoRoot, r)
	}
	return
}

// hourlyCosts buckets cost events by hour for the last `hours` hours.
// Returned slice has length `hours`, oldest first, newest last.
func hourlyCosts(repoRoot string, runs []runInfo, hours int) []float64 {
	if hours <= 0 {
		return nil
	}
	now := time.Now()
	cutoff := now.Add(-time.Duration(hours) * time.Hour).Truncate(time.Hour)
	buckets := make([]float64, hours)

	for _, r := range runs {
		path := filepath.Join(repoRoot, ".saturn", "runs", r.ID, "events.jsonl")
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			var ev struct {
				At  time.Time `json:"at"`
				Raw struct {
					Type string  `json:"type"`
					Cost float64 `json:"total_cost_usd"`
				} `json:"raw"`
			}
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				continue
			}
			if ev.Raw.Type != "result" || ev.Raw.Cost <= 0 {
				continue
			}
			if ev.At.Before(cutoff) {
				continue
			}
			idx := int(ev.At.Sub(cutoff) / time.Hour)
			if idx < 0 || idx >= hours {
				continue
			}
			buckets[idx] += ev.Raw.Cost
		}
		f.Close()
	}
	return buckets
}

func sparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	bars := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return strings.Repeat("·", len(values))
	}
	var b strings.Builder
	for _, v := range values {
		idx := int(v / max * float64(len(bars)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(bars) {
			idx = len(bars) - 1
		}
		b.WriteRune(bars[idx])
	}
	return b.String()
}
