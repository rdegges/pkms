// Package gitx shells out to system git with argv arrays — never shell
// strings (SPEC §13, §14). go-git was rejected: v6 is alpha and v5 has open
// perf/correctness issues on many-small-file worktrees.
package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MinVersion is the oldest git doctor accepts.
const (
	MinMajor = 2
	MinMinor = 30
)

// Git runs git commands inside one repository.
type Git struct {
	Dir string
}

func (g Git) run(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	// Never let cron-driven git hang on credential prompts.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// LookPath reports whether git is installed.
func LookPath() (string, error) { return exec.LookPath("git") }

var versionRe = regexp.MustCompile(`git version (\d+)\.(\d+)`)

// Version returns (major, minor, raw).
func Version() (int, int, string, error) {
	out, err := exec.Command("git", "version").Output()
	if err != nil {
		return 0, 0, "", err
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return 0, 0, strings.TrimSpace(string(out)), fmt.Errorf("cannot parse %q", out)
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	return maj, min, strings.TrimSpace(string(out)), nil
}

// VersionOK reports whether installed git meets MinMajor.MinMinor.
func VersionOK() (bool, string) {
	maj, min, raw, err := Version()
	if err != nil {
		return false, raw
	}
	return maj > MinMajor || (maj == MinMajor && min >= MinMinor), raw
}

// Init creates a repository in dir (no-op if one exists).
func Init(dir string) error {
	_, err := Git{Dir: dir}.run("init", "-q")
	return err
}

// IsRepo reports whether Dir is inside a git work tree.
func (g Git) IsRepo() bool {
	out, err := g.run("rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// HasCommits reports whether HEAD resolves.
func (g Git) HasCommits() bool {
	_, err := g.run("rev-parse", "--verify", "-q", "HEAD")
	return err == nil
}

// IsClean reports whether the worktree has no changes (tracked or untracked).
func (g Git) IsClean() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// OpInProgress reports a merge/rebase in progress (snapshot must skip).
func (g Git) OpInProgress() bool {
	gitDir, err := g.run("rev-parse", "--git-dir")
	if err != nil {
		return false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(g.Dir, gitDir)
	}
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply", "CHERRY_PICK_HEAD"} {
		if _, err := os.Stat(filepath.Join(gitDir, marker)); err == nil {
			return true
		}
	}
	return false
}

// AddAll stages everything (whole-state capture is the point — PLAN).
func (g Git) AddAll() error {
	_, err := g.run("add", "-A")
	return err
}

// identityArgs supplies a fallback identity so vault repos work without
// user git config, and disables signing/hooks: snapshots run from cron and
// must never block on a GPG pinentry or a failing hook.
func (g Git) identityArgs() []string {
	args := []string{"-c", "commit.gpgsign=false"}
	if email, err := g.run("config", "user.email"); err != nil || email == "" {
		args = append(args, "-c", "user.name=pkms", "-c", "user.email=pkms@localhost")
	}
	return args
}

// Commit commits staged changes and returns the new commit hash.
func (g Git) Commit(msg string) (string, error) {
	args := append(g.identityArgs(), "commit", "-q", "--no-verify", "-m", msg)
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return g.run("rev-parse", "HEAD")
}

// ChangedFileCount counts files in the staged diff against HEAD (or all
// staged files on the first commit).
func (g Git) ChangedFileCount() (int, error) {
	var out string
	var err error
	if g.HasCommits() {
		out, err = g.run("diff", "--cached", "--name-only")
	} else {
		out, err = g.run("diff", "--cached", "--name-only", "--no-renames")
	}
	if err != nil {
		return 0, err
	}
	if out == "" {
		return 0, nil
	}
	return len(strings.Split(out, "\n")), nil
}

// Push pushes HEAD to remote:refspec with --force-with-lease.
func (g Git) Push(remote, dstBranch string) error {
	_, err := g.run("push", "--force-with-lease", remote,
		fmt.Sprintf("HEAD:refs/heads/%s", dstBranch))
	return err
}

// HasRemote reports whether the named remote exists.
func (g Git) HasRemote(name string) bool {
	_, err := g.run("remote", "get-url", name)
	return err == nil
}

// SetRemote adds or updates a remote URL.
func (g Git) SetRemote(name, url string) error {
	if g.HasRemote(name) {
		_, err := g.run("remote", "set-url", name, url)
		return err
	}
	_, err := g.run("remote", "add", name, url)
	return err
}

// Log returns the newest n commits formatted with format.
func (g Git) Log(format string, n int) ([]string, error) {
	out, err := g.run("log", fmt.Sprintf("--format=%s", format), fmt.Sprintf("-%d", n))
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// RestorePaths restores the given paths to their state at ref, one path at
// a time so a missing-at-ref path (a file the op created) is deleted rather
// than aborting the whole restore.
func (g Git) RestorePaths(ref string, paths []string) error {
	for _, p := range paths {
		if _, err := g.run("cat-file", "-e", ref+":"+p); err == nil {
			if _, err := g.run("restore", "--source", ref, "--staged", "--worktree", "--", p); err != nil {
				return err
			}
			continue
		}
		if err := os.Remove(filepath.Join(g.Dir, filepath.FromSlash(p))); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ExistsAt reports whether path exists at ref.
func (g Git) ExistsAt(ref, path string) bool {
	_, err := g.run("cat-file", "-e", ref+":"+path)
	return err == nil
}
