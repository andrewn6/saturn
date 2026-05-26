package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCustomRunnerRunListLogsCleanup(t *testing.T) {
	repo := newTempRepo(t)
	withChdir(t, repo)
	isolatePath(t, "git", "sh", "cat")

	taskPath := filepath.Join(repo, "tasks", "custom-flow.md")
	mustWrite(t, taskPath, `---
id: custom-flow
title: Custom Flow
---

hello from task
`)

	runnerCmd := `cat > prompt.seen; printf '{"type":"assistant","subtype":"message"}\n'; printf 'plain text event\n'; printf '{"ok":true}\n'`
	if err := runCmd([]string{"--runner", runnerCmd, taskPath}); err != nil {
		t.Fatalf("runCmd: %v", err)
	}

	workdir := filepath.Join(repo, ".saturn", "wt", "custom-flow")
	if got := strings.TrimSpace(readFile(t, filepath.Join(workdir, "prompt.seen"))); got != "hello from task" {
		t.Fatalf("runner stdin = %q", got)
	}

	runDir := filepath.Join(repo, ".saturn", "runs", "custom-flow")
	for _, name := range []string{"events.jsonl", "result.json", "iterations.jsonl", "task.json", "phase"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(runDir, "phase"))); got != phaseDone {
		t.Fatalf("phase = %q, want %q", got, phaseDone)
	}

	var res resultSummaryForTest
	decodeJSON(t, filepath.Join(runDir, "result.json"), &res)
	if res.Backend != "custom" || res.Phase != "exec" || res.Iterations != 1 || res.StopReason != "max" || res.Error != "" {
		t.Fatalf("unexpected result: %+v", res)
	}

	events := readFile(t, filepath.Join(runDir, "events.jsonl"))
	for _, want := range []string{`"type":"assistant"`, `"subtype":"message"`, `plain text event`, `\"ok\":true`} {
		if !strings.Contains(events, want) {
			t.Fatalf("events missing %q in:\n%s", want, events)
		}
	}

	out, err := captureStdout(func() error { return listCmd(nil) })
	if err != nil {
		t.Fatalf("listCmd: %v", err)
	}
	if !strings.Contains(out, "custom-flow\tdone\tmax\t") {
		t.Fatalf("list output = %q", out)
	}

	out, err = captureStdout(func() error { return logsCmd([]string{"custom-flow"}) })
	if err != nil {
		t.Fatalf("logsCmd: %v", err)
	}
	if !strings.Contains(out, "plain text event") {
		t.Fatalf("logs output = %q", out)
	}

	out, err = captureStdout(func() error { return cleanupCmd([]string{"custom-flow"}) })
	if err != nil {
		t.Fatalf("cleanupCmd: %v", err)
	}
	if !strings.Contains(out, "cleaned custom-flow") {
		t.Fatalf("cleanup output = %q", out)
	}
	if _, err := os.Stat(workdir); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run dir still exists or stat failed: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "rev-parse", "--verify", "saturn/custom-flow").Run(); err == nil {
		t.Fatal("saturn/custom-flow branch still exists")
	}
}

func TestCustomRunnerFailureRecordsErrorAndLogs(t *testing.T) {
	repo := newTempRepo(t)
	withChdir(t, repo)
	isolatePath(t, "git", "sh")

	taskPath := filepath.Join(repo, "tasks", "failing-flow.md")
	mustWrite(t, taskPath, `---
id: failing-flow
title: Failing Flow
---

fail the task
`)

	err := runCmd([]string{"--runner", `printf '{"type":"assistant","subtype":"started"}\n'; exit 7`, taskPath})
	if err == nil || !strings.Contains(err.Error(), "1/1 tasks failed") {
		t.Fatalf("runCmd error = %v", err)
	}

	runDir := filepath.Join(repo, ".saturn", "runs", "failing-flow")
	var res resultSummaryForTest
	decodeJSON(t, filepath.Join(runDir, "result.json"), &res)
	if res.Backend != "custom" || res.Phase != "exec" || !strings.Contains(res.Error, "iteration 1 exited 7") {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(runDir, "phase"))); got != phaseExecuting {
		t.Fatalf("phase = %q, want %q", got, phaseExecuting)
	}

	out, err := captureStdout(func() error { return listCmd(nil) })
	if err != nil {
		t.Fatalf("listCmd: %v", err)
	}
	if !strings.Contains(out, "failing-flow\texecuting\terror\t") {
		t.Fatalf("list output = %q", out)
	}

	out, err = captureStdout(func() error { return logsCmd([]string{"failing-flow"}) })
	if err != nil {
		t.Fatalf("logsCmd: %v", err)
	}
	if !strings.Contains(out, `"subtype":"started"`) {
		t.Fatalf("logs output = %q", out)
	}
}

func TestPlanPhaseLogsInferCurrentPhase(t *testing.T) {
	repo := newTempRepo(t)
	withChdir(t, repo)
	isolatePath(t, "git", "sh")

	taskPath := filepath.Join(repo, "tasks", "planned-flow.md")
	mustWrite(t, taskPath, `---
id: planned-flow
title: Planned Flow
plan: true
---

plan the task
`)

	runnerCmd := `printf '# Plan\n' > PLAN.md; printf 'plan event\n'`
	if err := runCmd([]string{"--runner", runnerCmd, taskPath}); err != nil {
		t.Fatalf("runCmd: %v", err)
	}

	runDir := filepath.Join(repo, ".saturn", "runs", "planned-flow")
	if got := strings.TrimSpace(readFile(t, filepath.Join(runDir, "phase"))); got != phaseAwaitingPlan {
		t.Fatalf("phase = %q, want %q", got, phaseAwaitingPlan)
	}
	if _, err := os.Stat(filepath.Join(runDir, "events.plan.jsonl")); err != nil {
		t.Fatalf("missing plan events: %v", err)
	}

	out, err := captureStdout(func() error { return listCmd(nil) })
	if err != nil {
		t.Fatalf("listCmd: %v", err)
	}
	if !strings.Contains(out, "planned-flow\tawaiting_plan\tawaiting\t") {
		t.Fatalf("list output = %q", out)
	}

	out, err = captureStdout(func() error { return logsCmd([]string{"planned-flow"}) })
	if err != nil {
		t.Fatalf("logsCmd: %v", err)
	}
	if !strings.Contains(out, "plan event") {
		t.Fatalf("logs output = %q", out)
	}
}

type resultSummaryForTest struct {
	EndedAt    string `json:"ended_at"`
	Iterations int    `json:"iterations"`
	Backend    string `json:"backend"`
	Phase      string `json:"phase"`
	StopReason string `json:"stop_reason"`
	Error      string `json:"error"`
}

func newTempRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, "", "git", "init", "-b", "main", repo)
	mustWrite(t, filepath.Join(repo, "README.md"), "# test repo\n")
	run(t, repo, "git", "add", "README.md")
	run(t, repo, "git", "-c", "user.name=Saturn Tests", "-c", "user.email=saturn@example.invalid", "commit", "-m", "initial commit")
	if err := os.MkdirAll(filepath.Join(repo, "tasks"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

func run(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decodeJSON(t *testing.T, path string, dst any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func withChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func isolatePath(t *testing.T, names ...string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("lookpath %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
}

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	os.Stdout = w
	err = fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, copyErr := buf.ReadFrom(r)
	_ = r.Close()
	if err != nil {
		return buf.String(), err
	}
	return buf.String(), copyErr
}
