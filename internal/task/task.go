package task

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/andrewn6/saturn/internal/slug"
)

type Source int

const (
	SourceMarkdown Source = iota
	SourceTUI
	SourceGitHub
)

type Task struct {
	ID        string
	Title     string
	Prompt    string
	Source    Source
	Shared    bool
	Backend   string // "" = auto, "claude", "opencode"
	Model     string // "" = backend default; e.g. "opus", "anthropic/claude-opus-4-7", "openai/gpt-5"
	Variant   string // "" = backend default; e.g. "high", "medium", "low", "max", "minimal"
	Loop      bool   // false = single-shot (default), true = Ralph-style iterate
	Plan      bool   // true = produce PLAN.md and gate on human approval before execution
	Architect bool   // true = produce STACK.md (stack/trade-off analysis) and gate before plan/execute
}

// New finalizes a partially-populated Task, enforcing the same invariants
// ParseFile does: non-empty Prompt, non-empty Title (defaulting to
// "untitled"), and a deterministic ID derived from the title when not set.
//
// Use this anywhere a Task is built outside ParseFile — `saturn plan`
// emits tasks programmatically, GitHub ingestion synthesizes them, and a
// future TUI source will too. Keeping this in one place stops the
// invariants from drifting per source.
func New(t Task) (*Task, error) {
	if strings.TrimSpace(t.Prompt) == "" {
		return nil, fmt.Errorf("task has empty prompt")
	}
	if strings.TrimSpace(t.Title) == "" {
		t.Title = "untitled"
	}
	if strings.TrimSpace(t.ID) == "" {
		t.ID = slug.Make(t.Title)
		if t.ID == "" {
			t.ID = "untitled"
		}
	}
	return &t, nil
}

// ParseFile reads a task markdown file with optional YAML-ish front matter.
// Format:
//
//	---
//	id: my-task
//	shared: false
//	---
//	# Title line
//	Prompt body...
func ParseFile(path string) (*Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	t := &Task{Source: SourceMarkdown}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var body strings.Builder
	inFront := false
	lineNo := 0

	for sc.Scan() {
		line := sc.Text()
		lineNo++

		if lineNo == 1 && strings.TrimSpace(line) == "---" {
			inFront = true
			continue
		}
		if inFront {
			if strings.TrimSpace(line) == "---" {
				inFront = false
				continue
			}
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "id":
				t.ID = v
			case "title":
				t.Title = v
			case "shared":
				t.Shared = v == "true"
			case "backend":
				t.Backend = v
			case "model":
				t.Model = v
			case "variant":
				t.Variant = v
			case "loop":
				t.Loop = v == "true"
			case "plan":
				t.Plan = v == "true"
			case "architect":
				t.Architect = v == "true"
			}
			continue
		}

		if t.Title == "" && strings.HasPrefix(line, "# ") {
			t.Title = strings.TrimSpace(strings.TrimPrefix(line, "#"))
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	t.Prompt = strings.TrimSpace(body.String())
	out, err := New(*t)
	if err != nil {
		return nil, fmt.Errorf("task %q: %w", path, err)
	}
	return out, nil
}
