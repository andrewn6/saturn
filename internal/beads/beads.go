package beads

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Available reports whether the `bd` CLI is on PATH.
func Available() bool {
	_, err := exec.LookPath("bd")
	return err == nil
}

// Ensure initialises `.beads/` at repoRoot if it does not already exist.
// Silent no-op when bd is unavailable.
func Ensure(repoRoot string) error {
	if !Available() {
		return nil
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".beads")); err == nil {
		return nil
	}
	cmd := exec.Command("bd", "init", "--stealth")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd init: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Create makes a new bead with the given title and returns its id.
func Create(repoRoot, title string, labels []string) (string, error) {
	if !Available() {
		return "", nil
	}
	args := []string{"q", title}
	for _, l := range labels {
		args = append(args, "-l", l)
	}
	cmd := exec.Command("bd", args...)
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bd q: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Comment appends a comment to an existing bead.
func Comment(repoRoot, id, body string) error {
	if !Available() || id == "" {
		return nil
	}
	cmd := exec.Command("bd", "comment", id, body)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd comment: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Close marks a bead as closed.
func Close(repoRoot, id string) error {
	if !Available() || id == "" {
		return nil
	}
	cmd := exec.Command("bd", "close", id)
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd close: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
