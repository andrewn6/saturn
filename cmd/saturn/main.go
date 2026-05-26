package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewn6/saturn/internal/agent"
	"github.com/andrewn6/saturn/internal/assets"
	"github.com/andrewn6/saturn/internal/beads"
	"github.com/andrewn6/saturn/internal/gitops"
	"github.com/andrewn6/saturn/internal/loop"
	"github.com/andrewn6/saturn/internal/paths"
	"github.com/andrewn6/saturn/internal/runner"
	"github.com/andrewn6/saturn/internal/selfupdate"
	"github.com/andrewn6/saturn/internal/task"
	"github.com/andrewn6/saturn/internal/tui"
	"github.com/andrewn6/saturn/internal/worktree"
)

// version and commit are baked in at release-build time via goreleaser
// ldflags (see .goreleaser.yaml). Source builds get "dev"/"" so the upgrade
// flow always treats them as outdated and offers the latest tagged release.
var (
	version = "dev"
	commit  = ""
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		if err := initCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "plan":
		if err := planCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "run":
		if err := runCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "list":
		if err := listCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "logs":
		if err := logsCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "cleanup":
		if err := cleanupCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "watch":
		if err := watchCmd(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "merge":
		if err := mergeCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "approve":
		if err := approveCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "version", "--version", "-v":
		versionCmd()
	case "upgrade", "self-update":
		if err := upgradeCmd(); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func versionCmd() {
	fmt.Printf("saturn %s", version)
	if commit != "" {
		fmt.Printf(" (%s)", shortCommit(commit))
	}
	fmt.Println()
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}

// upgradeCmd replaces the running binary with the latest GitHub release.
// On success prints the new tag; on no-op prints the current. Errors bubble
// out so cmd handler exits non-zero (matches every other subcommand).
func upgradeCmd() error {
	fmt.Printf("checking for updates (current: %s)…\n", version)
	tag, did, err := selfupdate.Apply(version)
	if err != nil {
		return err
	}
	if !did {
		fmt.Printf("already on latest (%s)\n", tag)
		return nil
	}
	fmt.Printf("upgraded to %s\n", tag)
	return nil
}

func mergeCmd(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	base := fs.String("base", "main", "branch to merge into")
	skipCleanup := fs.Bool("no-cleanup", false, "leave worktree and branch in place after merge")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: saturn merge [--base main] [--no-cleanup] <task-id>")
	}
	taskID := fs.Arg(0)
	branch := "saturn/" + taskID

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}

	conflicts, err := gitops.Conflicts(root, *base, branch)
	if err != nil {
		return fmt.Errorf("preflight: %w", err)
	}
	if len(conflicts) > 0 {
		fmt.Fprintln(os.Stderr, "merge would conflict in:")
		for _, c := range conflicts {
			fmt.Fprintln(os.Stderr, "  ", c)
		}
		return fmt.Errorf("%d conflicting file(s); resolve manually then re-run", len(conflicts))
	}
	if err := gitops.Merge(root, *base, branch); err != nil {
		return err
	}
	fmt.Printf("merged %s into %s\n", branch, *base)
	if *skipCleanup {
		return nil
	}
	if err := gitops.Cleanup(root, taskID); err != nil {
		return fmt.Errorf("cleanup: %w (merge succeeded; run manually)", err)
	}
	fmt.Printf("removed worktree and branch %s\n", branch)
	return nil
}

func watchCmd() error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}
	return tui.Run(paths.RunsRoot(root), version)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  saturn init    [--force] [--no-example]")
	fmt.Fprintln(os.Stderr, "  saturn plan    [--out <dir>] [--shared] [--cleanup] [--max-iter N]")
	fmt.Fprintln(os.Stderr, "                 \"<idea>\" | --from <file.md>")
	fmt.Fprintln(os.Stderr, "  saturn run     [--max-iter N] [--parallel N] [--runner <cmd>] <task.md|owner/repo#N>...")
	fmt.Fprintln(os.Stderr, "  saturn list")
	fmt.Fprintln(os.Stderr, "  saturn logs    [--phase exec|plan|arch] <task-id>")
	fmt.Fprintln(os.Stderr, "  saturn cleanup <task-id>...")
	fmt.Fprintln(os.Stderr, "  saturn merge   [--base main] [--no-cleanup] <task-id>")
	fmt.Fprintln(os.Stderr, "  saturn approve <task-id>   (resume a plan-mode task after PLAN.md review)")
	fmt.Fprintln(os.Stderr, "  saturn watch")
	fmt.Fprintln(os.Stderr, "  saturn upgrade   (replace this binary with the latest GitHub release)")
	fmt.Fprintln(os.Stderr, "  saturn version")
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	maxIter := fs.Int("max-iter", 20, "max loop iterations per task (0 = unlimited)")
	parallel := fs.Int("parallel", 3, "max concurrent tasks")
	runnerCmd := fs.String("runner", "", "custom shell command; receives the prompt on stdin")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		usage()
		return fmt.Errorf("no tasks provided")
	}

	var tasks []*task.Task
	for _, p := range fs.Args() {
		var (
			t   *task.Task
			err error
		)
		if task.IsGitHubRef(p) {
			t, err = task.FromGitHub(p)
		} else {
			t, err = task.ParseFile(p)
		}
		if err != nil {
			return fmt.Errorf("load %s: %w", p, err)
		}
		if *runnerCmd != "" {
			t.Runner = *runnerCmd
		}
		tasks = append(tasks, t)
	}

	if len(tasks) > 1 {
		for _, t := range tasks {
			if t.Shared {
				return fmt.Errorf("task %q has shared:true — shared mode is only supported for single-task runs (multiple agents would collide on AGENT.md and file edits); remove shared:true to use worktrees", t.ID)
			}
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}

	if err := beads.Ensure(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: beads unavailable: %v\n", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	errs := make([]error, len(tasks))

	for i, t := range tasks {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t *task.Task) {
			defer wg.Done()
			defer func() { <-sem }()
			errs[i] = driveTask(ctx, root, t, *maxIter)
		}(i, t)
	}
	wg.Wait()

	var failed int
	for i, t := range tasks {
		if errs[i] != nil {
			failed++
			fmt.Fprintf(os.Stderr, "saturn: task=%s error: %v\n", t.ID, errs[i])
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d tasks failed", failed, len(tasks))
	}
	return nil
}

// Phase tags persisted to .saturn/runs/<id>/phase. The state machine for a
// task with both architect:true and plan:true is:
//
//	architecting → awaiting_stack → planning → awaiting_plan → executing → done
//
// Tasks with only plan:true skip the architect/stack states; tasks with
// neither flag run executing → done in one go. `saturn approve` advances
// whichever awaiting_* state is current to the next non-awaiting state.
const (
	phaseArchitecting  = "architecting"
	phaseAwaitingStack = "awaiting_stack"
	phasePlanning      = "planning"
	phaseAwaitingPlan  = "awaiting_plan"
	// phaseAwaitingApproval is kept for backward compat with on-disk state
	// from older builds — `saturn approve` treats it as awaiting_plan.
	phaseAwaitingApproval = "awaiting_approval"
	phaseExecuting        = "executing"
	phaseDone             = "done"
)

func driveTask(ctx context.Context, root string, t *task.Task, maxIter int) error {
	workdir := root
	branch := paths.Branch(t.ID)
	if !t.Shared {
		workdir = paths.Worktree(root, t.ID)
		if err := os.MkdirAll(filepath.Dir(workdir), 0o755); err != nil {
			return err
		}
		if err := worktree.Add(root, workdir, branch); err != nil {
			return err
		}
	}

	runDir := paths.RunDir(root, t.ID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}

	if err := loop.WriteAgentMD(workdir, t.Prompt); err != nil {
		return err
	}

	// Persist the task struct so `saturn approve` can resume without the
	// original markdown path.
	if tb, err := json.MarshalIndent(t, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "task.json"), tb, 0o644)
	}

	// Walk the state machine. Each gated phase (architect, plan) runs once,
	// flips the on-disk phase to its awaiting_* state, and returns — the user
	// resumes via `saturn approve`. The phase file is the single source of
	// truth so this function is safe to re-enter from `approveCmd`.
	ph := readPhase(runDir)

	if t.Architect {
		switch ph {
		case "", phaseArchitecting:
			if err := writePhase(runDir, phaseArchitecting); err != nil {
				return err
			}
			if err := runPhase(ctx, root, t, workdir, runDir, maxIter, phaseArchitecting); err != nil {
				return err
			}
			if err := writePhase(runDir, phaseAwaitingStack); err != nil {
				return err
			}
			fmt.Printf("[%s] STACK.md ready: %s\n", t.ID, filepath.Join(workdir, "STACK.md"))
			fmt.Printf("[%s] review then run: saturn approve %s\n", t.ID, t.ID)
			return nil
		case phaseAwaitingStack:
			return fmt.Errorf("task %s is awaiting stack approval; run: saturn approve %s", t.ID, t.ID)
		}
		// fall through: ph is past the architect gate
	}

	if t.Plan {
		switch ph {
		case "", phasePlanning, phaseArchitecting:
			// "" or phaseArchitecting can land here when architect is off, or
			// when approveCmd has just bumped us past awaiting_stack.
			if err := writePhase(runDir, phasePlanning); err != nil {
				return err
			}
			if err := runPhase(ctx, root, t, workdir, runDir, maxIter, phasePlanning); err != nil {
				return err
			}
			if err := writePhase(runDir, phaseAwaitingPlan); err != nil {
				return err
			}
			fmt.Printf("[%s] PLAN.md ready: %s\n", t.ID, filepath.Join(workdir, "PLAN.md"))
			fmt.Printf("[%s] review then run: saturn approve %s\n", t.ID, t.ID)
			return nil
		case phaseAwaitingPlan, phaseAwaitingApproval:
			return fmt.Errorf("task %s is awaiting plan approval; run: saturn approve %s", t.ID, t.ID)
		}
		// fall through: ph is past the plan gate
	}

	if err := writePhase(runDir, phaseExecuting); err != nil {
		return err
	}
	if err := runPhase(ctx, root, t, workdir, runDir, maxIter, phaseExecuting); err != nil {
		return err
	}
	_ = writePhase(runDir, phaseDone)
	return nil
}

// runPhase executes one phase (architect, plan, or execute). The phase tag
// drives:
//   - which standing prompt to send (architect.md / plan.md / task body)
//   - log + result filenames (events[.architect|.plan].jsonl, result*.json)
//   - whether to force single-shot (architect and plan ignore t.Loop)
//   - whether to close the beads issue at the end (only the exec phase does)
func runPhase(ctx context.Context, root string, t *task.Task, workdir, runDir string, maxIter int, phase string) error {
	beadID, err := beads.Create(root, t.Title, []string{"saturn", "task:" + t.ID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warn: bd create failed: %v\n", t.ID, err)
	}

	gated := phase == phaseArchitecting || phase == phasePlanning

	// Short tags drive log filenames and stdout prefixes; keep them stable
	// because TUI/external scripts may grep for them.
	var shortTag, logSuffix string
	switch phase {
	case phaseArchitecting:
		shortTag, logSuffix = "arch", ".architect"
	case phasePlanning:
		shortTag, logSuffix = "plan", ".plan"
	default:
		shortTag, logSuffix = "exec", ""
	}

	logFile, err := os.Create(filepath.Join(runDir, "events"+logSuffix+".jsonl"))
	if err != nil {
		return err
	}
	defer logFile.Close()
	enc := json.NewEncoder(logFile)
	var mu sync.Mutex

	fmt.Printf("[%s/%s] start workdir=%s\n", t.ID, shortTag, workdir)

	driveTaskCopy := *t
	standingPrompt := pickPrompt(t)
	switch phase {
	case phaseArchitecting:
		driveTaskCopy.Loop = false
		standingPrompt = assets.ArchitectPrompt
	case phasePlanning:
		driveTaskCopy.Loop = false
		standingPrompt = assets.PlanPrompt
	}

	sum, err := loop.Drive(ctx, loop.Options{
		Task:           &driveTaskCopy,
		Workdir:        workdir,
		RunDir:         runDir,
		StandingPrompt: standingPrompt,
		MaxIterations:  maxIter,
		BeadID:         beadID,
		OnEvent: func(iter int, ev runner.Event) {
			mu.Lock()
			_ = enc.Encode(ev)
			mu.Unlock()
			fmt.Printf("[%s/%s#%d %s] %s%s\n", t.ID, shortTag, iter, ev.At.Format("15:04:05"), ev.Type, suffix(ev.Subtype))
		},
	})

	res := map[string]any{
		"ended_at":   time.Now().Format(time.RFC3339),
		"iterations": 0,
		"backend":    backendName(t),
		"phase":      shortTag,
	}
	if sum != nil {
		res["iterations"] = len(sum.Iterations)
		res["stop_reason"] = sum.Reason
	}
	if err != nil {
		res["error"] = err.Error()
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "result"+logSuffix+".json"), b, 0o644)

	if err != nil {
		return err
	}
	// Only the executor phase resolves the beads issue. Architect/plan close
	// nothing because the work isn't done — they just produced an artifact.
	if !gated && sum.Reason == loop.StopEmpty {
		if cerr := beads.Close(root, beadID); cerr != nil {
			fmt.Fprintf(os.Stderr, "[%s] warn: bd close: %v\n", t.ID, cerr)
		}
	}
	fmt.Printf("[%s/%s] done iterations=%d stop=%s bead=%s\n", t.ID, shortTag, len(sum.Iterations), sum.Reason, beadID)
	return nil
}

func readPhase(runDir string) string {
	b, err := os.ReadFile(filepath.Join(runDir, "phase"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writePhase(runDir, p string) error {
	return os.WriteFile(filepath.Join(runDir, "phase"), []byte(p+"\n"), 0o644)
}

func approveCmd(args []string) error {
	fs := flag.NewFlagSet("approve", flag.ExitOnError)
	maxIter := fs.Int("max-iter", 20, "max loop iterations for execute phase (0 = unlimited)")
	taskFile := fs.String("task", "", "path to task markdown (optional; defaults to .saturn/runs/<id>/task.md if cached)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: saturn approve [--task <path>] <task-id>")
	}
	taskID := fs.Arg(0)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}
	runDir := paths.RunDir(root, taskID)
	ph := readPhase(runDir)

	var t *task.Task
	if *taskFile != "" {
		t, err = task.ParseFile(*taskFile)
		if err != nil {
			return fmt.Errorf("load task: %w", err)
		}
	} else {
		b, rerr := os.ReadFile(filepath.Join(runDir, "task.json"))
		if rerr != nil {
			return fmt.Errorf("no cached task at %s; pass --task <path>", filepath.Join(runDir, "task.json"))
		}
		t = &task.Task{}
		if jerr := json.Unmarshal(b, t); jerr != nil {
			return fmt.Errorf("decode cached task: %w", jerr)
		}
	}

	// Advance whichever gate is current. driveTask is re-entrant against
	// the on-disk phase, so we just need to bump it to the next non-awaiting
	// state and let driveTask take over.
	switch ph {
	case phaseAwaitingStack:
		// Stack approved. If plan is also requested, jump to planning;
		// otherwise straight to execute.
		if t.Plan {
			if err := writePhase(runDir, phasePlanning); err != nil {
				return err
			}
		} else {
			if err := writePhase(runDir, phaseExecuting); err != nil {
				return err
			}
		}
	case phaseAwaitingPlan, phaseAwaitingApproval:
		// Plan approved (or legacy awaiting_approval from older builds).
		if err := writePhase(runDir, phaseExecuting); err != nil {
			return err
		}
	default:
		return fmt.Errorf("task %s phase=%q; nothing to approve (expected %s or %s)",
			taskID, ph, phaseAwaitingStack, phaseAwaitingPlan)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := beads.Ensure(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: beads unavailable: %v\n", err)
	}
	return driveTask(ctx, root, t, *maxIter)
}

func listCmd(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: saturn list")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(paths.RunsRoot(root))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(paths.RunsRoot(root), e.Name())
		phase := readPhase(runDir)
		if phase == "" {
			phase = "unknown"
		}
		res := readResult(resultPathForPhase(runDir, phase))
		status := strings.TrimSpace(res.StopReason)
		if isAwaitingPhase(phase) {
			status = "awaiting"
		} else if res.Error != "" {
			status = "error"
		} else if status == "" && phase != phaseDone {
			status = "running"
		} else if status == "" {
			status = "done"
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", e.Name(), phase, status, res.EndedAt)
	}
	return nil
}

func logsCmd(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ExitOnError)
	phase := fs.String("phase", "", "log phase: exec, plan, or arch (default: infer from current phase)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: saturn logs [--phase exec|plan|arch] <task-id>")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	phaseName := *phase
	if phaseName == "" {
		phaseName = phaseForLog(readPhase(paths.RunDir(root, fs.Arg(0))))
	} else if !validLogPhase(phaseName) {
		return fmt.Errorf("unknown phase %q", phaseName)
	}
	logSuffix := logSuffixForPhase(phaseName)
	b, err := os.ReadFile(filepath.Join(paths.RunDir(root, fs.Arg(0)), "events"+logSuffix+".jsonl"))
	if err != nil && *phase == "" && logSuffix != "" {
		b, err = os.ReadFile(filepath.Join(paths.RunDir(root, fs.Arg(0)), "events.jsonl"))
	}
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(b)
	return err
}

func validLogPhase(phase string) bool {
	switch phase {
	case "exec", "plan", "arch", "architect":
		return true
	default:
		return false
	}
}

func logSuffixForPhase(phase string) string {
	switch phase {
	case "", "exec":
		return ""
	case "plan":
		return ".plan"
	case "arch", "architect":
		return ".architect"
	default:
		return ""
	}
}

func phaseForLog(phase string) string {
	switch phase {
	case phaseArchitecting, phaseAwaitingStack:
		return "arch"
	case phasePlanning, phaseAwaitingPlan, phaseAwaitingApproval:
		return "plan"
	default:
		return "exec"
	}
}

func cleanupCmd(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: saturn cleanup <task-id>...")
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	for _, id := range fs.Args() {
		runDir := paths.RunDir(root, id)
		if !isSharedRun(runDir) {
			if err := gitops.Cleanup(root, id); err != nil {
				return err
			}
		}
		if err := os.RemoveAll(runDir); err != nil {
			return fmt.Errorf("remove run state %s: %w", id, err)
		}
		fmt.Printf("cleaned %s\n", id)
	}
	return nil
}

type resultSummary struct {
	EndedAt    string `json:"ended_at"`
	StopReason string `json:"stop_reason"`
	Error      string `json:"error"`
}

func readResult(path string) resultSummary {
	var res resultSummary
	b, err := os.ReadFile(path)
	if err != nil {
		return res
	}
	_ = json.Unmarshal(b, &res)
	return res
}

func resultPathForPhase(runDir, phase string) string {
	switch phase {
	case phaseArchitecting, phaseAwaitingStack:
		return filepath.Join(runDir, "result.architect.json")
	case phasePlanning, phaseAwaitingPlan, phaseAwaitingApproval:
		return filepath.Join(runDir, "result.plan.json")
	default:
		return filepath.Join(runDir, "result.json")
	}
}

func isAwaitingPhase(phase string) bool {
	switch phase {
	case phaseAwaitingStack, phaseAwaitingPlan, phaseAwaitingApproval:
		return true
	default:
		return false
	}
}

func isSharedRun(runDir string) bool {
	b, err := os.ReadFile(filepath.Join(runDir, "task.json"))
	if err != nil {
		return false
	}
	var t task.Task
	if err := json.Unmarshal(b, &t); err != nil {
		return false
	}
	return t.Shared
}

func repoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return worktree.RepoRoot(cwd)
}

func backendName(t *task.Task) string {
	if t.Runner != "" {
		return "custom"
	}
	return agent.Resolve(t.Backend)
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "/" + s
}

func pickPrompt(t *task.Task) string {
	if t.Loop {
		return assets.StandingPrompt
	}
	// Single-shot: send the task body directly as the prompt. Wrapping in
	// a "read AGENT.md, do the work" preamble was making agents think the
	// task body was a prompt to acknowledge rather than a task to execute.
	return t.Prompt
}
