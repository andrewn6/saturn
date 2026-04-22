package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/andrewn6/saturn/internal/runner"
	"github.com/andrewn6/saturn/internal/task"
	"github.com/andrewn6/saturn/internal/worktree"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: saturn run <task.md>")
			os.Exit(2)
		}
		if err := runCmd(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: saturn run <task.md>")
}

func runCmd(taskPath string) error {
	t, err := task.ParseFile(taskPath)
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

	workdir := cwd
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
	logFile, err := os.Create(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return err
	}
	defer logFile.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("saturn: task=%s workdir=%s\n", t.ID, workdir)
	events, wait, err := runner.Run(ctx, t, workdir)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(logFile)
	for ev := range events {
		_ = enc.Encode(ev)
		fmt.Printf("[%s] %s%s\n", ev.At.Format(time.RFC3339), ev.Type, suffix(ev.Subtype))
	}

	res, err := wait()
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(map[string]any{
		"exit_code": res.ExitCode,
		"ended_at":  time.Now().Format(time.RFC3339),
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "result.json"), b, 0o644)

	fmt.Printf("saturn: done exit=%d events=%d\n", res.ExitCode, len(res.Events))
	return nil
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return "/" + s
}
