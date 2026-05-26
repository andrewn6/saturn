package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewn6/saturn/internal/paths"
	"github.com/andrewn6/saturn/internal/worktree"
)

// initCmd scaffolds the standard saturn layout inside the current repo:
//
//	tasks/                 (with optional example.md)
//	plans/                 (with a .gitkeep)
//	.saturn/.gitignore     (excludes runtime state from git)
//
// Idempotent: existing files are left alone unless --force is set. The
// command must be run inside a git repo (we use worktree.RepoRoot to
// resolve the top level), matching every other saturn subcommand.
func initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite scaffold files that already exist")
	noExample := fs.Bool("no-example", false, "skip writing tasks/example.md")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: saturn init [--force] [--no-example]")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := worktree.RepoRoot(cwd)
	if err != nil {
		return err
	}

	steps := []scaffoldStep{
		{
			path:    paths.TasksDir(root),
			dirOnly: true,
		},
		{
			path:    paths.PlansDir(root),
			dirOnly: true,
		},
		{
			// .gitkeep lets the plans/ dir survive a fresh git
			// checkout. Without it, an empty plans/ directory wouldn't
			// be tracked and users would land in a slightly different
			// state than `saturn init` produced.
			path:    filepath.Join(paths.PlansDir(root), ".gitkeep"),
			content: "",
		},
		{
			path:    filepath.Join(paths.SaturnDir(root), ".gitignore"),
			content: saturnGitignore,
		},
	}

	if !*noExample {
		steps = append(steps, scaffoldStep{
			path:    filepath.Join(paths.TasksDir(root), "example.md"),
			content: exampleTask,
		})
	}

	for _, s := range steps {
		if err := s.apply(*force); err != nil {
			return err
		}
	}

	// Patch the top-level .gitignore so runtime state doesn't accidentally
	// get committed. We append rather than overwriting because the user
	// almost certainly has their own ignores already.
	if err := patchRootGitignore(root); err != nil {
		// Non-fatal — the .saturn/.gitignore above covers most of it.
		fmt.Fprintf(os.Stderr, "warn: could not patch %s/.gitignore: %v\n", root, err)
	}

	fmt.Printf("saturn initialized in %s\n", root)
	fmt.Println("  tasks/        hand-authored task files go here")
	fmt.Println("  plans/        `saturn plan` writes generated batches here")
	fmt.Println("  .saturn/      runtime state (worktrees, run logs); git-ignored")
	if !*noExample {
		fmt.Println()
		fmt.Println("next: edit tasks/example.md, then `saturn run tasks/example.md`")
	} else {
		fmt.Println()
		fmt.Println("next: drop a task into tasks/, then `saturn run tasks/<file>.md`")
		fmt.Println("  or: `saturn plan \"<rough idea>\"` to generate one")
	}
	return nil
}

// scaffoldStep is one file or directory we want to exist post-init.
// content == "" + dirOnly == false means "create as empty file"; dirOnly
// means "MkdirAll only, don't touch contents."
type scaffoldStep struct {
	path    string
	content string
	dirOnly bool
}

func (s scaffoldStep) apply(force bool) error {
	if s.dirOnly {
		// MkdirAll is idempotent; no need for the force flag.
		if err := os.MkdirAll(s.path, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", s.path, err)
		}
		return nil
	}
	if _, err := os.Stat(s.path); err == nil && !force {
		// Exists; don't clobber. Matches what users expect from
		// `git init`, `npm init`, etc — re-running is safe.
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", s.path, err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of %s: %w", s.path, err)
	}
	if err := os.WriteFile(s.path, []byte(s.content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", s.path, err)
	}
	return nil
}

// patchRootGitignore appends saturn's runtime-state ignores to the repo's
// top-level .gitignore if those lines aren't already present. We don't
// touch any other lines — only append, only what's missing.
func patchRootGitignore(repoRoot string) error {
	path := filepath.Join(repoRoot, ".gitignore")
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := []string{
		".saturn/runs/",
		".saturn/wt/",
		".saturn/tui-spawned.log",
		".saturn/memory.md",
	}
	var toAppend []string
	body := string(existing)
	for _, ln := range lines {
		if !containsLine(body, ln) {
			toAppend = append(toAppend, ln)
		}
	}
	if len(toAppend) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString(body)
	if len(body) > 0 && !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	if len(body) > 0 {
		b.WriteString("\n# saturn\n")
	} else {
		b.WriteString("# saturn\n")
	}
	for _, ln := range toAppend {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// containsLine is a whole-line substring check — avoids matching ".saturn/runs/"
// inside a longer pattern like "!.saturn/runs/keep-me".
func containsLine(haystack, line string) bool {
	for _, ln := range strings.Split(haystack, "\n") {
		if strings.TrimSpace(ln) == line {
			return true
		}
	}
	return false
}

// saturnGitignore is dropped into .saturn/.gitignore. It covers the
// runtime-state subdirs so even users who don't patch the root .gitignore
// won't accidentally commit worktrees or run logs.
const saturnGitignore = `# saturn runtime state — regenerated on every run
runs/
wt/
tui-spawned.log
memory.md
`

// exampleTask is the starter task `saturn init` writes (unless --no-example).
// Kept short and self-explanatory; uses front-matter syntax so it doubles
// as a reference for the format.
const exampleTask = `---
id: example
title: Example task
---
# Example task

Replace this with what you actually want done. A task body is just a
prompt to the agent. For multi-step work prefer a checklist:

- [ ] step one
- [ ] step two
- [ ] step three

When the agent finishes (no unchecked items left) saturn stops the loop.

Run with:

    saturn run tasks/example.md

Then review the branch ` + "`saturn/example`" + ` and merge it back:

    saturn merge example
`
