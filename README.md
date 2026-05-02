# saturn

Run multiple coding agents in parallel, each isolated in its own git worktree.

Saturn takes a markdown task file (or a GitHub issue reference), spins up a
dedicated worktree on a `saturn/<task-id>` branch, drives an agent backend
(`claude` or `opencode`) inside it, and streams the run to a TUI you can
attach to. When the agent reports done, you `saturn merge` the branch back.

---

## Install

### One-liner (macOS / Linux, amd64 or arm64)

```sh
curl -fsSL https://raw.githubusercontent.com/andrewn6/saturn/main/install.sh | sh
```

Pin a version or change the install dir:

```sh
VERSION=v0.2.0 sh install.sh
INSTALL_DIR=$HOME/.local/bin sh install.sh
```

### From source

Requires Go 1.26.2+.

```sh
git clone https://github.com/andrewn6/saturn
cd saturn
go build -o saturn ./cmd/saturn
```

### Runtime requirements

- `git` — worktrees and merges
- `tmux` — for the watch UI's attach feature
- At least one agent backend on `PATH`: `claude` or `opencode`
- (optional) `bd` — Saturn opens/closes beads issues for each task if installed

---

## Usage

A task is a markdown file with optional front matter:

```markdown
---
id: fix-login
title: Fix login redirect
backend: opencode    # claude | opencode | "" (auto)
loop: false          # true = Ralph-style iterate until done
plan: false          # true = produce PLAN.md and gate on human approval
shared: false        # true = run in repo root instead of a worktree
---
# Fix login redirect

Users hitting /login while authenticated should be redirected to /dashboard.
...
```

Run one or many tasks (each in its own worktree, up to `--parallel` at a time):

```sh
saturn run tasks/fix-login.md tasks/add-metrics.md
saturn run --parallel 5 --max-iter 30 tasks/*.md
saturn run andrewn6/saturn#42        # ingest a GitHub issue as a task
```

Watch live runs in a TUI (attach into the agent's tmux session, view diffs,
tail events):

```sh
saturn watch
```

Merge a finished task back into `main` (preflight-checks for conflicts, then
removes the worktree and branch):

```sh
saturn merge fix-login
saturn merge --base develop --no-cleanup fix-login
```

Run artifacts land in `.saturn/runs/<task-id>/` (`events.jsonl`, `result.json`),
worktrees in `.saturn/wt/<task-id>/`.

---

## Features

Currently shipping:

- [x] Markdown task files with YAML-ish front matter (`id`, `title`, `backend`, `loop`, `plan`, `shared`)
- [x] GitHub issue ingestion (`owner/repo#N`) as a task source
- [x] Per-task git worktrees on `saturn/<task-id>` branches
- [x] Parallel task execution with a `--parallel` semaphore
- [x] Pluggable agent backends: `claude` and `opencode` (auto-detected)
- [x] Single-shot and Ralph-style loop modes (`loop: true`, `--max-iter`)
- [x] Plan-gated mode (`plan: true`) — agent writes `PLAN.md` first
- [x] Bubble Tea TUI (`saturn watch`) with live event tailing and diff view
- [x] tmux-backed agent sessions you can attach to mid-run
- [x] `saturn merge` with conflict preflight and worktree/branch cleanup
- [x] Per-run JSONL event log and `result.json` summary
- [x] Optional [beads](https://github.com/) integration — issues opened/closed per task
- [x] Self-update path (`internal/selfupdate`)
- [x] Shared mode for single-task runs that need to operate on the repo root
- [x] One-line installer + goreleaser builds for `linux`/`darwin` × `amd64`/`arm64`

Roadmap / wanted:

- [ ] `saturn init` to scaffold `tasks/` and config
- [ ] Resume / replay of an interrupted run from `events.jsonl`
- [ ] Cost & token accounting per task
- [ ] More backends (Aider, Codex CLI, Gemini CLI, custom exec)
- [ ] Auto-PR on successful merge (`gh pr create` integration)
- [ ] Conflict-aware scheduling — detect overlapping file sets before launching
- [ ] Task dependencies / DAGs (`depends_on:` in front matter)
- [ ] Real YAML front matter parser (current one is hand-rolled, no nested keys)
- [ ] TUI: per-task log filtering, search, copy-to-clipboard for diffs
- [ ] Reaper for orphaned worktrees from crashed runs
- [ ] Windows support
- [ ] Test suite and CI

---

## Contributing

PRs welcome. The repo is small and opinionated; please skim `AGENTS.md` before
sending changes — it documents the conventions the existing code follows
(stdlib-first, hand-rolled parsing, `git` via `os/exec`, errors wrapped with
trimmed `CombinedOutput`, etc.).

Quick loop:

```sh
go build ./...
go vet ./...
go test ./...
gofmt -w .
```

Guidelines:

- Keep the dependency graph minimal. Charm libraries for the TUI are fine;
  pulling in a YAML or git library is not, unless the task forces it.
- New packages go under `internal/`. One file per package until it grows out
  of that.
- Match the existing error style: `fmt.Errorf("<verb>: %w: %s", err, output)`.
- No `context.Context` half-plumbed — add it at the boundary or not at all.
- If you add tooling (lint, CI, release steps), document it in `AGENTS.md`.

Bug reports and feature requests: open a GitHub issue. If it's a task Saturn
itself could attempt, even better — drop it in `tasks/` and `saturn run` it.

---

## License

See `LICENSE` (TBD).
