// Package tmux is a thin wrapper around the `tmux` CLI. Saturn uses tmux as
// a reliable host for interactive agent sessions: each task gets a detached
// tmux session that can be attached/detached from the watch TUI without
// embedding a terminal emulator.
package tmux

import (
	"fmt"
	"os/exec"
	"strings"
)

// Available reports whether the tmux binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// SessionExists reports whether a tmux session with the given name exists.
func SessionExists(name string) bool {
	cmd := exec.Command("tmux", "has-session", "-t", name)
	return cmd.Run() == nil
}

// NewDetached creates a new detached tmux session running shellCmd in workdir.
// shellCmd is passed as a single argument to `tmux new-session -d -c ... <cmd>`.
func NewDetached(name, workdir, shellCmd string) error {
	if !Available() {
		return fmt.Errorf("tmux not installed")
	}
	args := []string{"new-session", "-d", "-s", name}
	if workdir != "" {
		args = append(args, "-c", workdir)
	}
	args = append(args, shellCmd)
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux new-session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AttachCmd returns an *exec.Cmd that attaches to the named session. Intended
// to be handed to tea.ExecProcess so the outer TUI suspends for the duration.
func AttachCmd(name string) *exec.Cmd {
	return exec.Command("tmux", "attach-session", "-t", name)
}

// KillSession terminates the named session if it exists. No-op otherwise.
func KillSession(name string) error {
	if !SessionExists(name) {
		return nil
	}
	cmd := exec.Command("tmux", "kill-session", "-t", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux kill-session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
