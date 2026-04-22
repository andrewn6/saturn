package worktree

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Add creates a git worktree at dir based on branch (created fresh from HEAD).
// If branch already exists, it is checked out as-is.
func Add(repoRoot, dir, branch string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "add", "-b", branch, abs, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "already exists") {
			cmd = exec.Command("git", "-C", repoRoot, "worktree", "add", abs, branch)
			if out2, err2 := cmd.CombinedOutput(); err2 != nil {
				return fmt.Errorf("worktree add: %w: %s", err2, strings.TrimSpace(string(out2)))
			}
			return nil
		}
		return fmt.Errorf("worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Remove force-removes a worktree directory.
func Remove(repoRoot, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", abs)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree remove: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RepoRoot resolves the git top-level for the given start path.
func RepoRoot(start string) (string, error) {
	cmd := exec.Command("git", "-C", start, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repo: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
