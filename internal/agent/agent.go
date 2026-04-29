// Package agent abstracts which CLI tool drives an iteration. Saturn
// supports `claude` and `opencode`; per-task choice via task front matter
// `backend:` field, with auto-detect fallback (opencode preferred).
package agent

import (
	"fmt"
	"os/exec"
)

const (
	BackendClaude   = "claude"
	BackendOpencode = "opencode"
)

// Available reports whether the named backend's binary is on PATH.
func Available(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// Resolve returns name if non-empty, otherwise picks the default
// (opencode if available, else claude).
func Resolve(name string) string {
	if name != "" {
		return name
	}
	if Available(BackendOpencode) {
		return BackendOpencode
	}
	return BackendClaude
}

// SpawnCmd builds the headless run command for the given backend.
func SpawnCmd(backend, prompt, workdir string) (*exec.Cmd, error) {
	switch Resolve(backend) {
	case BackendOpencode:
		cmd := exec.Command("opencode", "run", prompt,
			"--format", "json",
			"--dangerously-skip-permissions")
		cmd.Dir = workdir
		return cmd, nil
	case BackendClaude:
		cmd := exec.Command("claude",
			"-p", prompt,
			"--output-format", "stream-json",
			"--verbose",
			"--dangerously-skip-permissions")
		cmd.Dir = workdir
		return cmd, nil
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}

// AttachCmd builds the interactive resume command for an existing session.
func AttachCmd(backend, sessionID string) *exec.Cmd {
	switch Resolve(backend) {
	case BackendOpencode:
		return exec.Command("opencode", "run", "-s", sessionID)
	case BackendClaude:
		return exec.Command("claude", "--resume", sessionID)
	}
	return exec.Command("claude", "--resume", sessionID)
}
