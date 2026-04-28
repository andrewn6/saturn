// Package tmux is a thin wrapper around the `tmux` CLI. Saturn uses tmux as
// a reliable host for interactive agent sessions: each task gets a detached
// tmux session that can be attached/detached from the watch TUI without
// embedding a terminal emulator.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Saturn uses an isolated tmux server (socket "saturn") so our custom
// keybinds (F12=detach, F10=kill) don't pollute the user's normal tmux.
const socket = "saturn"

const tmuxConfig = `# Saturn tmux config — single-chord detach/kill (no prefix)
# Ctrl+\ detaches (returns to Saturn), Ctrl+] kills the session.
bind-key -n 'C-\' detach-client
bind-key -n C-] kill-session
set -g status off
set -g mouse on
`

var (
	configOnce sync.Once
	configPath string
	configErr  error
)

func ensureConfig() (string, error) {
	configOnce.Do(func() {
		configPath = filepath.Join(os.TempDir(), "saturn-tmux.conf")
		configErr = os.WriteFile(configPath, []byte(tmuxConfig), 0o644)
	})
	return configPath, configErr
}

func base() ([]string, error) {
	cfg, err := ensureConfig()
	if err != nil {
		return nil, err
	}
	return []string{"-L", socket, "-f", cfg}, nil
}

// Available reports whether the tmux binary is on PATH.
func Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

// SessionExists reports whether a tmux session with the given name exists.
func SessionExists(name string) bool {
	args, err := base()
	if err != nil {
		return false
	}
	args = append(args, "has-session", "-t", name)
	cmd := exec.Command("tmux", args...)
	return cmd.Run() == nil
}

// NewDetached creates a new detached tmux session running shellCmd in workdir.
// shellCmd is passed as a single argument to `tmux new-session -d -c ... <cmd>`.
func NewDetached(name, workdir, shellCmd string) error {
	if !Available() {
		return fmt.Errorf("tmux not installed")
	}
	args, err := base()
	if err != nil {
		return err
	}
	args = append(args, "new-session", "-d", "-s", name)
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
	args, err := base()
	if err != nil {
		return exec.Command("tmux", "attach-session", "-t", name)
	}
	args = append(args, "attach-session", "-t", name)
	return exec.Command("tmux", args...)
}

// KillSession terminates the named session if it exists. No-op otherwise.
func KillSession(name string) error {
	if !SessionExists(name) {
		return nil
	}
	args, err := base()
	if err != nil {
		return err
	}
	args = append(args, "kill-session", "-t", name)
	cmd := exec.Command("tmux", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux kill-session: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
