package task

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var ghRefRe = regexp.MustCompile(`^([^/\s]+)/([^/\s#]+)#(\d+)$`)

// IsGitHubRef reports whether s looks like "owner/repo#123".
func IsGitHubRef(s string) bool {
	return ghRefRe.MatchString(s)
}

// FromGitHub resolves "owner/repo#123" to a Task via the local `gh` CLI.
// Requires `gh` to be authenticated.
func FromGitHub(ref string) (*Task, error) {
	m := ghRefRe.FindStringSubmatch(ref)
	if m == nil {
		return nil, fmt.Errorf("not a github ref: %q", ref)
	}
	owner, repo, num := m[1], m[2], m[3]

	cmd := exec.Command("gh", "issue", "view", num,
		"-R", owner+"/"+repo,
		"--json", "number,title,body")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue view: %w", err)
	}
	var payload struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse gh json: %w", err)
	}
	body := strings.TrimSpace(payload.Body)
	if body == "" {
		return nil, fmt.Errorf("issue %s has empty body", ref)
	}
	return &Task{
		ID:     fmt.Sprintf("%s-%s-%d", owner, repo, payload.Number),
		Title:  payload.Title,
		Prompt: body,
		Source: SourceGitHub,
	}, nil
}
