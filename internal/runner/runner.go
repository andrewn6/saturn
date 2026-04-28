package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/andrewn6/saturn/internal/agent"
	"github.com/andrewn6/saturn/internal/task"
)

// Event is one decoded line from claude's stream-json output,
// plus local receive metadata.
type Event struct {
	At      time.Time       `json:"at"`
	Raw     json.RawMessage `json:"raw"`
	Type    string          `json:"type,omitempty"`
	Subtype string          `json:"subtype,omitempty"`
}

type Result struct {
	ExitCode int
	Events   []Event
}

// Run launches `claude -p <prompt> --output-format stream-json --verbose` in
// workdir, streaming each parsed event to the returned channel. The channel
// closes when the subprocess exits. Call the returned wait func to collect
// the exit code and the full event slice.
func Run(ctx context.Context, t *task.Task, workdir string) (<-chan Event, func() (*Result, error), error) {
	cmd, err := agent.SpawnCmd(t.Backend, t.Prompt, workdir)
	if err != nil {
		return nil, nil, err
	}
	// Re-bind to ctx so cancel kills the child.
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	cmd.Dir = workdir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start claude: %w", err)
	}

	events := make(chan Event, 64)
	var collected []Event

	go func() {
		defer close(events)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			ev := Event{At: time.Now(), Raw: append(json.RawMessage(nil), line...)}
			var peek struct {
				Type    string `json:"type"`
				Subtype string `json:"subtype"`
			}
			_ = json.Unmarshal(line, &peek)
			ev.Type = peek.Type
			ev.Subtype = peek.Subtype
			collected = append(collected, ev)
			events <- ev
		}
	}()

	wait := func() (*Result, error) {
		err := cmd.Wait()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				return nil, err
			}
		}
		return &Result{ExitCode: code, Events: collected}, nil
	}

	return events, wait, nil
}
