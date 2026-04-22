# AGENTS.md

## Status

Very early-stage Go project. At time of writing the repo contains only:

```
go.mod
internal/task/task.go
internal/worktree/worktree.go
```

There is no `cmd/` entry point, no `main` package, no tests, no CI, no Makefile, and no README. Do not assume infrastructure that isn't here — add it only when the task explicitly requires it, and prefer the conventions in the existing two files.

## Module

- Module path: `github.com/andrewn6/saturn`
- Go version: **1.26.2** (see `go.mod`). This is newer than most toolchains ship; if `go build` complains about the toolchain, do not downgrade the directive — it's intentional.
- No third-party dependencies yet. Keep the dependency graph empty unless a task forces otherwise; prefer the standard library (the existing code shells out to `git` via `os/exec` rather than pulling in a git library, and parses front-matter by hand rather than importing a YAML package — follow that style).

## Commands

Only the standard Go toolchain is wired up:

```sh
go build ./...
go vet ./...
go test ./...      # no tests exist yet; exits 0
gofmt -w .
```

There is no lint config, no test runner script, no release process. If you add one, document it here.

## Domain model

Saturn appears to be a tool for running multiple coding-agent "tasks" in parallel, each isolated in its own **git worktree / branch**. The two packages reflect that split:

### `internal/task`

Parses a task definition. A task is a markdown file with optional YAML-ish front matter:

```
---
id: my-task
title: Some title
shared: false
---
# Title line
Prompt body...
```

Key behaviors to preserve if you extend this:

- Front matter is only recognized when the **very first line** is `---`. The parser is hand-rolled (`bufio.Scanner` + `strings.Cut` on `:`), not a real YAML parser — don't add quoted values, nested keys, or lists without upgrading the parser.
- Recognized front-matter keys: `id`, `title`, `shared`. Unknown keys are silently ignored.
- `shared` is parsed as `v == "true"` (case-sensitive, no `yes`/`1`).
- If `title` is missing from front matter, the first `# ` heading in the body becomes the title (and is stripped from the prompt).
- If `id` is missing, it's derived by `slugify(title)`: lowercased, non-`[a-z0-9]` runs collapsed to `-`, trimmed. Keep this deterministic — other code likely uses the id as a directory/branch name.
- Empty prompt is an error; missing title defaults to `"untitled"`.
- Scanner buffer is bumped to 1 MiB to tolerate long prompt lines — keep that if you touch `ParseFile`.
- `Source` is an iota enum (`SourceMarkdown`, `SourceTUI`, `SourceGitHub`). `ParseFile` only ever sets `SourceMarkdown`; the TUI and GitHub sources are placeholders for not-yet-written ingestion paths.

### `internal/worktree`

Thin wrapper around `git worktree` that always shells out (`exec.Command("git", "-C", repoRoot, ...)`). Notable behavior:

- `Add(repoRoot, dir, branch)` first tries `git worktree add -b <branch> <abs> HEAD` (create a new branch from HEAD). If git reports `already exists` in its output, it **retries** with `git worktree add <abs> <branch>` to check out the existing branch. This string-sniffing fallback is load-bearing — don't remove it without replacing the behavior.
- `Remove` uses `--force`; callers don't get a chance to recover uncommitted work in the worktree.
- `dir` is always converted to an absolute path before being handed to git.
- Errors wrap the underlying error *and* include trimmed `CombinedOutput` so messages surface git's stderr.
- `RepoRoot(start)` runs `git rev-parse --show-toplevel` and is the canonical way to resolve `repoRoot` — use it rather than walking up for `.git` yourself.

## Conventions observed

- Package layout: everything lives under `internal/` (not importable by outside modules). Continue placing new packages there unless a deliberate public API is being introduced.
- One file per package so far, package name matches directory name.
- Errors are formatted with `fmt.Errorf("<verb>: %w: %s", err, <git output>)` — match this shape when adding new `git`/`exec` wrappers so error messages stay consistent.
- No logging library; functions return errors and let the caller decide.
- Comments on exported identifiers follow standard Go doc style (`// Add creates ...`). Keep it.
- No context.Context plumbing yet. If you add long-running operations (network, subprocess), introduce `context.Context` at the boundary rather than sprinkling it partially.

## Gotchas

- The `go.mod` `go 1.26.2` line will break older toolchains — don't "fix" it.
- `worktree.Add`'s "already exists" branch depends on git's English error text. If you localize or change git versions, this fallback silently stops working.
- `task.ParseFile` treats the first `# ` heading as the title *only if* `t.Title` is still empty, i.e. front-matter `title:` wins. Body heading is then dropped from the prompt; subsequent `# ` headings are preserved.
- `slugify` does not enforce a max length or uniqueness. If you start using the id as a filesystem/branch name, collisions and overlong titles are your problem to handle at the call site.
- `shared` defaulting to `false` and being parsed only as the literal string `"true"` means typos silently become `false`.

## When extending

- New ingestion sources (TUI, GitHub) should produce a `*task.Task` with the appropriate `Source` value and satisfy the same invariants `ParseFile` enforces (non-empty `Prompt`, non-empty `Title`, deterministic `ID`). Consider factoring those invariants into a shared constructor before adding the second source.
- Anything that creates a worktree per task should pair `worktree.Add` with a deferred/cleanup `worktree.Remove` — there's no reaper yet.
