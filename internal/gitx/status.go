package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Status is local recovery evidence, not a record of snapshot execution.
type Status struct {
	Available           bool
	Detail              string
	Commit              string
	CommitAt            string
	Dirty               bool
	OperationInProgress bool
}

// ReadStatus inspects a vault-root repository without optional index writes,
// credential prompts, remote access, or mutation commands.
func (g Git) ReadStatus() (Status, error) {
	var s Status
	if _, err := os.Stat(g.Dir); err != nil {
		return s, err
	}
	if _, err := os.Lstat(filepath.Join(g.Dir, ".git")); os.IsNotExist(err) {
		s.Detail = "vault has no git repository; initialize local recovery history"
		return s, nil
	} else if err != nil {
		return s, err
	}
	if _, err := LookPath(); err != nil {
		s.Detail = "git is unavailable; local recovery history could not be inspected"
		return s, nil
	}
	top, err := g.statusRead("rev-parse", "--show-toplevel")
	if err != nil {
		return s, err
	}
	realTop, err := filepath.EvalSymlinks(top)
	if err != nil {
		return s, err
	}
	realDir, err := filepath.EvalSymlinks(g.Dir)
	if err != nil {
		return s, err
	}
	if realTop != realDir {
		s.Detail = "vault is not the repository root; local recovery history is unavailable"
		return s, nil
	}
	head, err := g.statusRead("rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			s.Detail = "repository has no commits; create a local recovery point"
			return s, nil
		}
		return s, err
	}
	date, err := g.statusRead("show", "-s", "--format=%cI", head)
	if err != nil {
		return s, err
	}
	worktree, err := g.statusRead("status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return s, err
	}
	gitDir, err := g.statusRead("rev-parse", "--absolute-git-dir")
	if err != nil {
		return s, err
	}
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			s.OperationInProgress = true
		} else if !os.IsNotExist(err) {
			return s, err
		}
	}
	s.Available, s.Commit, s.CommitAt, s.Dirty = true, head, date, worktree != ""
	return s, nil
}

func (g Git) statusRead(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-C", g.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		// Do not expose arbitrary repository configuration or command stderr.
		return "", fmt.Errorf("git %s inspection: %w", args[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
