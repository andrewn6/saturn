// Package gitops handles merging an agent's saturn/<id> branch back into
// the base (typically main) and cleaning up the worktree afterward.
package gitops

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Conflicts returns the list of paths that would conflict if `branch` were
// merged into `base`. Empty list (and nil error) means a clean merge is
// possible. Uses `git merge-tree` so nothing is mutated.
func Conflicts(repoRoot, base, branch string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoRoot, "merge-tree",
		"--write-tree", "--name-only", base, branch)
	out, err := cmd.Output()
	if err == nil {
		return nil, nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return nil, fmt.Errorf("merge-tree: %w", err)
	}
	if exitErr.ExitCode() != 1 {
		return nil, fmt.Errorf("merge-tree: %s", strings.TrimSpace(string(exitErr.Stderr)))
	}
	// Exit 1 = conflicts; stdout has tree-OID, blank line, then conflicting paths.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var conflicts []string
	pastBlank := false
	for _, ln := range lines {
		if !pastBlank {
			if ln == "" {
				pastBlank = true
			}
			continue
		}
		if ln != "" {
			conflicts = append(conflicts, ln)
		}
	}
	if len(conflicts) == 0 {
		conflicts = strings.Split(strings.TrimSpace(string(exitErr.Stderr)), "\n")
	}
	return conflicts, nil
}

// Merge does a --no-ff merge of branch into base. Switches to base first
// if not already on it. Caller must have already verified no conflicts.
func Merge(repoRoot, base, branch string) error {
	cur, err := currentBranch(repoRoot)
	if err != nil {
		return err
	}
	if cur != base {
		out, err := exec.Command("git", "-C", repoRoot, "checkout", base).CombinedOutput()
		if err != nil {
			return fmt.Errorf("checkout %s: %w: %s", base, err, strings.TrimSpace(string(out)))
		}
	}
	out, err := exec.Command("git", "-C", repoRoot, "merge", "--no-ff", "-m",
		"saturn: merge "+branch, branch).CombinedOutput()
	if err != nil {
		return fmt.Errorf("merge %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Cleanup removes the worktree and deletes the agent branch after a
// successful merge. Best-effort: returns the first hard error encountered.
func Cleanup(repoRoot, taskID string) error {
	wt := filepath.Join(repoRoot, ".saturn", "wt", taskID)
	branch := "saturn/" + taskID

	if out, err := exec.Command("git", "-C", repoRoot, "worktree", "remove", "--force", wt).
		CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "is not a working tree") {
			_ = exec.Command("git", "-C", repoRoot, "worktree", "prune").Run()
		}
	}
	if out, err := exec.Command("git", "-C", repoRoot, "branch", "-D", branch).
		CombinedOutput(); err != nil {
		return fmt.Errorf("delete branch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func currentBranch(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
