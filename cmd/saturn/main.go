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
	"github.com/andrewn6/saturn/internal/runner"
	"github.com/andrewn6/saturn/internal/task"
	"github.com/andrewn6/saturn/internal/tui"
	"github.com/andrewn6/saturn/internal/worktree"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		if err := runCmd(os.Args[2:]); err != nil {
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
	default:
		usage()
		os.Exit(2)
	}
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
	return tui.Run(filepath.Join(root, ".saturn", "runs"))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  saturn run   [--max-iter N] [--parallel N] <task.md|owner/repo#N>...")
	fmt.Fprintln(os.Stderr, "  saturn merge   [--base main] [--no-cleanup] <task-id>")
	fmt.Fprintln(os.Stderr, "  saturn approve <task-id>   (resume a plan-mode task after PLAN.md review)")
	fmt.Fprintln(os.Stderr, "  saturn watch")
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	maxIter := fs.Int("max-iter", 20, "max loop iterations per task (0 = unlimited)")
	parallel := fs.Int("parallel", 3, "max concurrent tasks")
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

const (
	phasePlanning   = "planning"
	phaseAwaiting   = "awaiting_approval"
	phaseExecuting  = "executing"
	phaseDone       = "done"
)

func driveTask(ctx context.Context, root string, t *task.Task, maxIter int) error {
	workdir := root
	branch := "saturn/" + t.ID
	if !t.Shared {
		workdir = filepath.Join(root, ".saturn", "wt", t.ID)
		if err := worktree.Add(root, workdir, branch); err != nil {
			return err
		}
	}

	runDir := filepath.Join(root, ".saturn", "runs", t.ID)
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

	// Plan-mode routing: produce PLAN.md and stop until human approves.
	if t.Plan {
		ph := readPhase(runDir)
		switch ph {
		case "", phasePlanning:
			if err := writePhase(runDir, phasePlanning); err != nil {
				return err
			}
			if err := runPhase(ctx, root, t, workdir, runDir, maxIter, true); err != nil {
				return err
			}
			if err := writePhase(runDir, phaseAwaiting); err != nil {
				return err
			}
			fmt.Printf("[%s] PLAN.md ready: %s\n", t.ID, filepath.Join(workdir, "PLAN.md"))
			fmt.Printf("[%s] review then run: saturn approve %s\n", t.ID, t.ID)
			return nil
		case phaseAwaiting:
			return fmt.Errorf("task %s is awaiting approval; run: saturn approve %s", t.ID, t.ID)
		case phaseExecuting, phaseDone:
			// fall through to execute phase
		}
	}

	if err := runPhase(ctx, root, t, workdir, runDir, maxIter, false); err != nil {
		return err
	}
	if t.Plan {
		_ = writePhase(runDir, phaseDone)
	}
	return nil
}

func runPhase(ctx context.Context, root string, t *task.Task, workdir, runDir string, maxIter int, planning bool) error {
	beadID, err := beads.Create(root, t.Title, []string{"saturn", "task:" + t.ID})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warn: bd create failed: %v\n", t.ID, err)
	}

	logName := "events.jsonl"
	if planning {
		logName = "events.plan.jsonl"
	}
	logFile, err := os.Create(filepath.Join(runDir, logName))
	if err != nil {
		return err
	}
	defer logFile.Close()
	enc := json.NewEncoder(logFile)
	var mu sync.Mutex

	phaseTag := "exec"
	if planning {
		phaseTag = "plan"
	}
	fmt.Printf("[%s/%s] start workdir=%s\n", t.ID, phaseTag, workdir)

	driveTaskCopy := *t
	standingPrompt := pickPrompt(t)
	if planning {
		// Plan phase always runs as a single iteration regardless of t.Loop.
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
			fmt.Printf("[%s/%s#%d %s] %s%s\n", t.ID, phaseTag, iter, ev.At.Format("15:04:05"), ev.Type, suffix(ev.Subtype))
		},
	})

	res := map[string]any{
		"ended_at":   time.Now().Format(time.RFC3339),
		"iterations": 0,
		"backend":    agent.Resolve(t.Backend),
		"phase":      phaseTag,
	}
	if sum != nil {
		res["iterations"] = len(sum.Iterations)
		res["stop_reason"] = sum.Reason
	}
	if err != nil {
		res["error"] = err.Error()
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	resultName := "result.json"
	if planning {
		resultName = "result.plan.json"
	}
	_ = os.WriteFile(filepath.Join(runDir, resultName), b, 0o644)

	if err != nil {
		return err
	}
	if !planning && sum.Reason == loop.StopEmpty {
		if cerr := beads.Close(root, beadID); cerr != nil {
			fmt.Fprintf(os.Stderr, "[%s] warn: bd close: %v\n", t.ID, cerr)
		}
	}
	fmt.Printf("[%s/%s] done iterations=%d stop=%s bead=%s\n", t.ID, phaseTag, len(sum.Iterations), sum.Reason, beadID)
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
	runDir := filepath.Join(root, ".saturn", "runs", taskID)
	ph := readPhase(runDir)
	if ph != phaseAwaiting {
		return fmt.Errorf("task %s phase=%q (need %q); nothing to approve", taskID, ph, phaseAwaiting)
	}

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

	if err := writePhase(runDir, phaseExecuting); err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := beads.Ensure(root); err != nil {
		fmt.Fprintf(os.Stderr, "warn: beads unavailable: %v\n", err)
	}
	return driveTask(ctx, root, t, *maxIter)
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
