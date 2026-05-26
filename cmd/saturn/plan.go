package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andrewn6/saturn/internal/agent"
	"github.com/andrewn6/saturn/internal/assets"
	"github.com/andrewn6/saturn/internal/loop"
	"github.com/andrewn6/saturn/internal/paths"
	"github.com/andrewn6/saturn/internal/runner"
	"github.com/andrewn6/saturn/internal/slug"
	"github.com/andrewn6/saturn/internal/task"
	"github.com/andrewn6/saturn/internal/worktree"
)

// planCmd runs the planning agent in a throwaway worktree to convert a
// rough idea into one or more saturn task files.
//
// The flow is:
//
//  1. Resolve the idea: either positional args concatenated as a string,
//     or the contents of --from <file>.
//  2. Derive a slug. The slug is used for the worktree dir
//     (.saturn/wt/_plan-<slug>/), the planning branch
//     (saturn/_plan-<slug>), and the output directory (plans/<slug>/ by
//     default).
//  3. Create the worktree (unless --shared, in which case we run against
//     repo root — useful when the planner needs to see uncommitted state).
//  4. Write the idea as AGENT.md in the worktree. The planner prompt
//     (internal/assets/planner.md) instructs the agent to read AGENT.md
//     and emit task files into out/.
//  5. Drive the agent single-shot via loop.Drive with PlannerPrompt as
//     the standing prompt.
//  6. Copy out/* from the worktree into the user-chosen output dir on
//     the repo root checkout, so the human can review and `saturn run`
//     the files without cd'ing into the planning worktree.
//  7. Leave the worktree in place by default (so the user can `cd` in
//     and inspect or re-run the planner), unless --cleanup is set.
//
// We deliberately do NOT touch .saturn/runs/ here — planning is not a
// task and shouldn't show up in `saturn watch`. The events log lives
// next to the output dir so a curious user can still trace what the
// planner did.
func planCmd(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	from := fs.String("from", "", "read the idea from this file instead of positional args")
	outDir := fs.String("out", "", "destination dir for generated task files (default: plans/<slug>/)")
	shared := fs.Bool("shared", false, "run planner in repo root instead of a fresh worktree")
	cleanup := fs.Bool("cleanup", false, "remove the planning worktree after copying output (default: keep)")
	maxIter := fs.Int("max-iter", 1, "max planner iterations (planning is single-shot by design; >1 only useful with --loop)")
	loopMode := fs.Bool("loop", false, "let the planner iterate (rare; default off — planning is one shot)")
	backend := fs.String("backend", "", "agent backend (\"\"|claude|opencode)")
	model := fs.String("model", "", "specific model id (backend-dependent)")
	variant := fs.String("variant", "", "reasoning effort tier (backend-dependent)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	idea, err := resolveIdea(*from, fs.Args())
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}

	// Slug seed: prefer the --from filename when present (planners run
	// repeatedly on the same idea file should produce deterministic
	// output dirs); fall back to a prefix of the idea text. The
	// date prefix keeps multiple plans for the same idea distinguishable
	// in plans/ listing — useful when iterating.
	seed := *from
	if seed != "" {
		seed = strings.TrimSuffix(filepath.Base(seed), filepath.Ext(seed))
	} else {
		seed = firstWords(idea, 6)
	}
	dateSlug := time.Now().Format("2006-01-02") + "-" + slug.MakeOrFallback(seed, "plan")

	workdir := root
	branch := paths.PlanBranch(dateSlug)
	if !*shared {
		workdir = paths.PlanWorktree(root, dateSlug)
		if err := worktree.Add(root, workdir, branch); err != nil {
			return fmt.Errorf("planner worktree: %w", err)
		}
	}

	if err := loop.WriteAgentMD(workdir, idea); err != nil {
		return err
	}

	// Output dir resolution:
	//   --out absolute  → use as-is
	//   --out relative  → relative to repo root, so `saturn plan --out tasks/foo`
	//                     ends up under <root>/tasks/foo regardless of cwd
	//   --out empty     → default plans/<date-slug>/
	dest := *outDir
	if dest == "" {
		dest = paths.PlanDir(root, dateSlug)
	} else if !filepath.IsAbs(dest) {
		dest = filepath.Join(root, dest)
	}

	// Synthesize a Task so we can reuse the runner+loop machinery. Loop
	// here is "planning runs once" — same convention as the existing
	// planning phase inside driveTask.
	plannerTask, err := task.New(task.Task{
		ID:      "_plan-" + dateSlug,
		Title:   "plan: " + firstWords(idea, 8),
		Prompt:  idea,
		Source:  task.SourceMarkdown,
		Backend: *backend,
		Model:   *model,
		Variant: *variant,
		Loop:    *loopMode,
	})
	if err != nil {
		return err
	}

	// Events go next to the destination so the trail of *how* a plan
	// was produced lives with the output. We don't use .saturn/runs/
	// because planning is meta-work, not a task.
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(dest, "planner.events.jsonl")
	logFile, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer logFile.Close()
	enc := json.NewEncoder(logFile)
	var mu sync.Mutex

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("[plan/%s] backend=%s workdir=%s\n", dateSlug, agent.Resolve(*backend), workdir)
	fmt.Printf("[plan/%s] idea: %s\n", dateSlug, firstWords(idea, 16))

	sum, err := loop.Drive(ctx, loop.Options{
		Task:           plannerTask,
		Workdir:        workdir,
		RunDir:         dest, // iterations.jsonl ends up alongside the output too
		StandingPrompt: assets.PlannerPrompt,
		MaxIterations:  *maxIter,
		OnEvent: func(iter int, ev runner.Event) {
			mu.Lock()
			_ = enc.Encode(ev)
			mu.Unlock()
			fmt.Printf("[plan/%s#%d %s] %s%s\n", dateSlug, iter, ev.At.Format("15:04:05"), ev.Type, suffix(ev.Subtype))
		},
	})
	if err != nil {
		return fmt.Errorf("planner agent: %w", err)
	}

	// Harvest the agent's output. The prompt tells it to write into
	// `out/` relative to the worktree; we copy that into the user-facing
	// destination dir. If the agent wrote a PLAN.md at out/PLAN.md it
	// gets carried over too.
	srcOut := filepath.Join(workdir, "out")
	copied, err := copyTree(srcOut, dest)
	if err != nil {
		return fmt.Errorf("harvest planner output (%s): %w", srcOut, err)
	}

	// Summary write so a future `saturn plan --resume` (not implemented
	// yet, but the data shape is ready) or external scripts can find
	// what was produced without re-globbing.
	resPath := filepath.Join(dest, "planner.result.json")
	res := map[string]any{
		"ended_at":    time.Now().Format(time.RFC3339),
		"iterations":  len(sum.Iterations),
		"stop_reason": string(sum.Reason),
		"backend":     agent.Resolve(*backend),
		"slug":        dateSlug,
		"branch":      branch,
		"worktree":    workdir,
		"files":       copied,
	}
	if b, mErr := json.MarshalIndent(res, "", "  "); mErr == nil {
		_ = os.WriteFile(resPath, b, 0o644)
	}

	// Worktree disposition. Default is to keep so the user can inspect
	// (`cd .saturn/wt/_plan-<slug>` and look at the full conversation
	// log, the agent's notes, etc). --cleanup removes it.
	if *cleanup && !*shared {
		if err := worktree.Remove(root, workdir); err != nil {
			// Non-fatal: harvested files are already on disk.
			fmt.Fprintf(os.Stderr, "warn: could not remove planner worktree: %v\n", err)
		}
	}

	// Report. Print task files first (the actionable output), then any
	// meta files. The hint at the bottom is what most users will copy.
	taskFiles, otherFiles := splitTaskFiles(copied)
	if len(taskFiles) == 0 {
		fmt.Printf("[plan/%s] planner produced no task files; check %s for blockers\n", dateSlug, dest)
		return nil
	}

	fmt.Println()
	fmt.Printf("[plan/%s] %d task file(s) in %s:\n", dateSlug, len(taskFiles), dest)
	for _, p := range taskFiles {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		fmt.Println("  " + rel)
	}
	if len(otherFiles) > 0 {
		fmt.Println("plus:")
		for _, p := range otherFiles {
			rel, _ := filepath.Rel(root, p)
			if rel == "" {
				rel = p
			}
			fmt.Println("  " + rel)
		}
	}
	fmt.Println()
	relDest, _ := filepath.Rel(root, dest)
	if relDest == "" {
		relDest = dest
	}
	fmt.Printf("review then run:\n  saturn run %s/*.md\n", relDest)
	return nil
}

// resolveIdea returns the idea text from either --from <file> or
// positional args. Exactly one source must be set; both empty is a usage
// error.
func resolveIdea(from string, pos []string) (string, error) {
	if from != "" && len(pos) > 0 {
		return "", fmt.Errorf("pass either --from <file> or a positional idea, not both")
	}
	if from != "" {
		b, err := os.ReadFile(from)
		if err != nil {
			return "", fmt.Errorf("read --from: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return "", fmt.Errorf("--from file %s is empty", from)
		}
		return string(b), nil
	}
	if len(pos) == 0 {
		return "", fmt.Errorf("usage: saturn plan [flags] \"<idea>\" | --from <file.md>")
	}
	idea := strings.TrimSpace(strings.Join(pos, " "))
	if idea == "" {
		return "", fmt.Errorf("idea is empty")
	}
	return idea, nil
}

// firstWords returns roughly the first n whitespace-delimited words of s,
// collapsed onto a single line. Used for slugs and log lines.
func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// copyTree copies every file under src into dst (recursively), preserving
// relative paths. Returns the list of destination paths written. If src
// doesn't exist, returns nil with no error — the planner is allowed to
// emit zero files (e.g. a blockers-only result), and the caller surfaces
// that case.
func copyTree(src, dst string) ([]string, error) {
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return nil, err
	}
	var written []string
	walkErr := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		written = append(written, target)
		return nil
	})
	return written, walkErr
}

// copyFile copies src to dst, creating dst if needed. Mode is fixed at
// 0o644 — the planner produces markdown, not executables.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

// splitTaskFiles partitions copied output into "task files" (anything
// directly under dest that ends in .md and isn't PLAN.md) and "other
// files" (PLAN.md, anything in subdirs). Used to make the final summary
// readable; only the task files are what the user actually runs.
func splitTaskFiles(all []string) (tasks, other []string) {
	for _, p := range all {
		base := filepath.Base(p)
		if strings.EqualFold(base, "PLAN.md") {
			other = append(other, p)
			continue
		}
		if strings.HasSuffix(strings.ToLower(base), ".md") {
			tasks = append(tasks, p)
			continue
		}
		other = append(other, p)
	}
	return
}
