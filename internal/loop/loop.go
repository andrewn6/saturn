package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/andrewn6/saturn/internal/runner"
	"github.com/andrewn6/saturn/internal/task"
)

type Options struct {
	Task           *task.Task
	Workdir        string
	RunDir         string
	StandingPrompt string
	MaxIterations  int
	BeadID         string
	OnEvent        func(iter int, ev runner.Event)
}

type StopReason string

const (
	StopSentinel StopReason = "sentinel"
	StopEmpty    StopReason = "empty"
	StopMax      StopReason = "max"
	StopCancel   StopReason = "cancel"
	StopError    StopReason = "error"
)

type IterRecord struct {
	Iter       int        `json:"iter"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    time.Time  `json:"ended_at"`
	ExitCode   int        `json:"exit_code"`
	StopReason StopReason `json:"stop_reason,omitempty"`
}

type Summary struct {
	Iterations []IterRecord
	Reason     StopReason
}

var uncheckedRe = regexp.MustCompile(`(?m)^\s*-\s\[\s\]`)

// Drive runs the task's standing-prompt loop in opts.Workdir until a stop
// condition fires.
func Drive(ctx context.Context, opts Options) (*Summary, error) {
	if opts.StandingPrompt == "" {
		return nil, fmt.Errorf("loop: empty standing prompt")
	}
	promptTask := *opts.Task
	promptTask.Prompt = opts.StandingPrompt

	iterLog, err := os.Create(filepath.Join(opts.RunDir, "iterations.jsonl"))
	if err != nil {
		return nil, err
	}
	defer iterLog.Close()
	enc := json.NewEncoder(iterLog)

	// Clear any stale STOP sentinel from a previous run on this worktree.
	_ = os.Remove(filepath.Join(opts.Workdir, "STOP"))

	// Single-shot tasks always run exactly one iteration regardless of input.
	singleShot := opts.Task != nil && !opts.Task.Loop
	if singleShot {
		opts.MaxIterations = 1
	}

	sum := &Summary{}
	iter := 0
	for {
		if err := ctx.Err(); err != nil {
			sum.Reason = StopCancel
			return sum, nil
		}
		if stopExists(opts.Workdir) {
			sum.Reason = StopSentinel
			return sum, nil
		}
		// Empty-checklist exit only applies in loop mode. Single-shot tasks
		// don't manage a checklist; they finish via max-iter=1 below.
		if !singleShot && iter > 0 && !hasUnchecked(opts.Workdir) {
			sum.Reason = StopEmpty
			return sum, nil
		}
		if opts.MaxIterations > 0 && iter >= opts.MaxIterations {
			sum.Reason = StopMax
			return sum, nil
		}

		iter++
		rec := IterRecord{Iter: iter, StartedAt: time.Now()}

		if opts.BeadID != "" {
			_ = os.Setenv("SATURN_BEAD_ID", opts.BeadID)
		}

		events, wait, err := runner.Run(ctx, &promptTask, opts.Workdir)
		if err != nil {
			rec.EndedAt = time.Now()
			rec.StopReason = StopError
			_ = enc.Encode(rec)
			sum.Reason = StopError
			return sum, err
		}
		for ev := range events {
			if opts.OnEvent != nil {
				opts.OnEvent(iter, ev)
			}
		}
		res, werr := wait()
		rec.EndedAt = time.Now()
		if werr != nil {
			rec.StopReason = StopError
			_ = enc.Encode(rec)
			sum.Iterations = append(sum.Iterations, rec)
			sum.Reason = StopError
			return sum, werr
		}
		rec.ExitCode = res.ExitCode
		_ = enc.Encode(rec)
		sum.Iterations = append(sum.Iterations, rec)
		if res.ExitCode != 0 {
			sum.Reason = StopError
			return sum, fmt.Errorf("iteration %d exited %d", iter, res.ExitCode)
		}
	}
}

func stopExists(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "STOP"))
	return err == nil
}

func hasUnchecked(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "AGENT.md"))
	if err != nil {
		return false
	}
	return uncheckedRe.MatchString(string(b))
}

// WriteAgentMD writes the task body as AGENT.md in workdir only if absent,
// so mid-run checklist state survives across Saturn restarts.
func WriteAgentMD(workdir, body string) error {
	p := filepath.Join(workdir, "AGENT.md")
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return os.WriteFile(p, []byte(strings.TrimSpace(body)+"\n"), 0o644)
}
