package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Status is local recovery evidence, not a record of snapshot execution.
type Status struct {
	Available           bool
	Detail              string
	Commit              string
	CommitAt            string
	Dirty               *bool
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
	promisor, err := g.hasStatusConfig(`^(extensions\.partialclone|remote\..*\.promisor)$`)
	if err != nil {
		return s, err
	}
	if promisor {
		s.Detail = "partial-clone/promisor repository: local recovery state was not inspected"
		return s, nil
	}
	head, err := g.statusRead("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		// A missing symbolic branch is an unborn repository. A broken ref,
		// missing object, detached invalid HEAD, or non-commit is corruption.
		if ref, refErr := g.statusRead("symbolic-ref", "--quiet", "HEAD"); refErr == nil {
			_, refErr = g.statusRead("show-ref", "--verify", "--quiet", "--", ref)
			if statusExit(refErr, 1) {
				s.Detail = "repository has no commits; create a local recovery point"
				return s, nil
			}
		}
		return s, err
	}
	date, err := g.statusRead("show", "--no-show-signature", "-s", "--format=%cI", head)
	if err != nil {
		return s, err
	}
	if _, err := time.Parse(time.RFC3339, date); err != nil {
		return s, fmt.Errorf("git HEAD commit has an invalid committer timestamp")
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
	filters, err := g.hasStatusConfig(`^filter\..*\.(clean|process)$`)
	if err != nil {
		return s, err
	}
	if filters {
		s.Detail = "working-tree changes are unknown: Git clean/process filters are configured"
	} else {
		index, err := g.statusRead("ls-files", "--stage", "-z")
		if err != nil {
			return s, err
		}
		for _, entry := range strings.Split(index, "\x00") {
			if strings.HasPrefix(entry, "160000 ") {
				s.Detail = "working-tree changes are unknown: repository contains submodules"
				break
			}
		}
		if s.Detail == "" {
			worktree, err := g.statusRead("status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=all")
			if err != nil {
				return s, err
			}
			dirty := worktree != ""
			s.Dirty = &dirty
		}
	}
	s.Available, s.Commit, s.CommitAt = true, head, date
	return s, nil
}

func statusExit(err error, code int) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == code
}

// Read names only: configured filter commands and remote URLs stay private.
func (g Git) hasStatusConfig(pattern string) (bool, error) {
	names, err := g.statusRead("config", "--name-only", "--get-regexp", pattern)
	if statusExit(err, 1) {
		return false, nil
	}
	return names != "", err
}

func (g Git) statusRead(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager", "--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "log.showSignature=false", "-C", g.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=")
	out, err := cmd.Output()
	if err != nil {
		// Do not expose arbitrary repository configuration or command stderr.
		return "", fmt.Errorf("git %s inspection: %w", args[0], err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
