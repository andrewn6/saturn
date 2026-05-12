// Package paths centralizes the on-disk layout saturn writes to inside a
// repo. Every consumer used to rebuild these paths inline with
// filepath.Join(repoRoot, ".saturn", …) — a maintenance hazard once the
// tree grows beyond runs/ and wt/. Treat this file as the single source of
// truth: anything new under .saturn/ should land here first, then be used
// by callers.
//
// Conventions:
//
//	<repoRoot>/
//	  tasks/                  human-authored task .md files
//	  plans/                  saturn-generated task batches (one dir per plan)
//	  .saturn/
//	    config                optional key=value defaults (see DefaultConfig)
//	    memory.md             shared agent scratchpad (written by agents)
//	    tui-spawned.log       stdout of `saturn run`/`approve` spawned by TUI
//	    runs/<task-id>/       per-task event log, result.json, phase, task.json
//	    wt/<task-id>/         per-task git worktree on branch saturn/<task-id>
//
// Nothing here touches the filesystem; helpers are pure path math so they
// can be used during dry-runs (saturn init --dry-run, future flags).
package paths

import "path/filepath"

// SaturnDir returns <repoRoot>/.saturn — the umbrella for all runtime state
// saturn manages itself (worktrees, run logs, memory, etc).
func SaturnDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".saturn")
}

// TasksDir is the conventional home for human-authored task markdown.
// Saturn doesn't *require* tasks live here (the run subcommand takes any
// path) but the TUI's "new task" form and `saturn init` both default to
// it, and `saturn plan` defaults its output to a sibling directory.
func TasksDir(repoRoot string) string {
	return filepath.Join(repoRoot, "tasks")
}

// PlansDir is where `saturn plan` drops generated task batches by default.
// Each invocation creates a subdirectory under this (PlanDir below) so
// multiple plans coexist and can be diffed/compared.
func PlansDir(repoRoot string) string {
	return filepath.Join(repoRoot, "plans")
}

// PlanDir returns the directory for a single plan invocation, e.g.
// <repoRoot>/plans/2025-05-11-auth/. The slug should be deterministic
// (see internal/slug) so re-running the same idea overwrites rather than
// proliferates plan dirs.
func PlanDir(repoRoot, slug string) string {
	return filepath.Join(PlansDir(repoRoot), slug)
}

// ConfigFile is the optional key=value defaults file. Absent file is fine
// (callers should treat ENOENT as "use built-in defaults"). The format is
// deliberately not YAML — see DefaultConfig for the schema.
func ConfigFile(repoRoot string) string {
	return filepath.Join(SaturnDir(repoRoot), "config")
}

// MemoryFile is the shared scratchpad agents append observations to. The
// standing/plan/architect prompts all read it as advisory context.
func MemoryFile(repoRoot string) string {
	return filepath.Join(SaturnDir(repoRoot), "memory.md")
}

// TUISpawnedLog captures stdout/stderr of `saturn run` / `saturn approve`
// invocations spawned from inside the TUI (which can't share a stdio
// stream with the parent Bubble Tea program).
func TUISpawnedLog(repoRoot string) string {
	return filepath.Join(SaturnDir(repoRoot), "tui-spawned.log")
}

// RunsRoot is <repoRoot>/.saturn/runs — the parent of every per-task run
// directory. Passed to the TUI so it can list runs without scanning the
// full .saturn/ tree.
func RunsRoot(repoRoot string) string {
	return filepath.Join(SaturnDir(repoRoot), "runs")
}

// RunDir returns the per-task run directory under .saturn/runs/.
// Contains: phase, task.json, events*.jsonl, result*.json, iterations.jsonl.
func RunDir(repoRoot, taskID string) string {
	return filepath.Join(RunsRoot(repoRoot), taskID)
}

// WorktreesRoot is <repoRoot>/.saturn/wt — parent of every per-task
// worktree.
func WorktreesRoot(repoRoot string) string {
	return filepath.Join(SaturnDir(repoRoot), "wt")
}

// Worktree returns the per-task worktree path under .saturn/wt/.
// Used by both the driver (creating it) and gitops.Cleanup (removing it).
func Worktree(repoRoot, taskID string) string {
	return filepath.Join(WorktreesRoot(repoRoot), taskID)
}

// Branch is the canonical git branch name saturn uses for a task worktree.
// Centralized here so the "saturn/" prefix isn't sprinkled across the
// codebase — change it once if we ever rename it.
func Branch(taskID string) string {
	return "saturn/" + taskID
}

// PlanBranch is the branch used for a `saturn plan` worktree. The
// underscore prefix keeps plan branches sorted away from task branches in
// `git branch` output, and makes them easy to spot/clean up.
func PlanBranch(slug string) string {
	return "saturn/_plan-" + slug
}

// PlanWorktree is the per-plan worktree path under .saturn/wt/.
// Mirrors Worktree but with the _plan- prefix in the directory name as
// well, so a future reaper can distinguish plan worktrees from task ones.
func PlanWorktree(repoRoot, slug string) string {
	return filepath.Join(WorktreesRoot(repoRoot), "_plan-"+slug)
}

// RepoRootFromRunsRoot recovers the repo root from a RunsRoot path. Used
// by the TUI which is handed only the runs root for historical reasons.
// Keep this paired with RunsRoot — if the layout changes, both move.
func RepoRootFromRunsRoot(runsRoot string) string {
	return filepath.Dir(filepath.Dir(runsRoot))
}
