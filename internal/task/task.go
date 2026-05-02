package task

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type Source int

const (
	SourceMarkdown Source = iota
	SourceTUI
	SourceGitHub
)

type Task struct {
	ID      string
	Title   string
	Prompt  string
	Source  Source
	Shared  bool
	Backend string // "" = auto, "claude", "opencode"
	Loop    bool   // false = single-shot (default), true = Ralph-style iterate
	Plan    bool   // true = produce PLAN.md and gate on human approval before execution
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
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
			case "loop":
				t.Loop = v == "true"
			case "plan":
				t.Plan = v == "true"
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
	if t.Prompt == "" {
		return nil, fmt.Errorf("task %q has empty prompt", path)
	}
	if t.Title == "" {
		t.Title = "untitled"
	}
	if t.ID == "" {
		t.ID = slugify(t.Title)
	}
	return t, nil
}
