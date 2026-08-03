package snapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
)

func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	require.NoError(t, err, string(out))
	return string(out)
}

var t0 = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// newVault creates a git-initialized vault with one committed note.
func newVault(t *testing.T) *config.Vault {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	require.NoError(t, gitx.Init(dir))
	write(t, dir, "Note.md", "hello\n")
	g := gitx.Git{Dir: dir}
	require.NoError(t, g.AddAll())
	_, err := g.Commit("pkms init")
	require.NoError(t, err)
	return &config.Vault{Name: "test", Path: dir, Profile: "para"}
}

func write(t *testing.T, root string, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

func TestTakeCommitsAndSkipsClean(t *testing.T) {
	v := newVault(t)

	r := Take(v, t0)
	require.Equal(t, "clean", r.Status)

	write(t, v.Path, "New.md", "new\n")
	write(t, v.Path, "Note.md", "edited\n")
	r = Take(v, t0)
	require.Equal(t, "committed", r.Status)
	require.Equal(t, 2, r.FileCount)

	g := gitx.Git{Dir: v.Path}
	subjects, err := g.Log("%s", 1)
	require.NoError(t, err)
	require.Equal(t, "snapshot: 2 file(s) @ 2026-08-03T12:00:00Z", subjects[0])
}

func TestTakeSkipsDuringMerge(t *testing.T) {
	v := newVault(t)
	write(t, v.Path, "New.md", "x")
	require.NoError(t, os.WriteFile(filepath.Join(v.Path, ".git", "MERGE_HEAD"), []byte("dead"), 0o644))

	r := Take(v, t0)
	require.Equal(t, "skipped-merge", r.Status)
}

func TestTakePushesToPerHostBranch(t *testing.T) {
	v := newVault(t)
	remote := t.TempDir()
	rawGit(t, remote, "init", "--bare", "-q")
	v.Snapshot.Remote = remote

	write(t, v.Path, "New.md", "x")
	r := Take(v, t0)
	require.Equal(t, "committed", r.Status)
	require.True(t, r.Pushed, r.PushError)

	out := rawGit(t, remote, "branch", "--list")
	require.Contains(t, out, "snapshots/"+Hostname())
}

func TestOpUndoRevertsOnlyOpFiles(t *testing.T) {
	v := newVault(t)
	write(t, v.Path, "Untracked.md", "user edit in flight\n")

	op, err := Begin(v, "lint-fix", t0)
	require.NoError(t, err)

	// The pre-commit captured the in-flight user edit.
	g := gitx.Git{Dir: v.Path}
	subjects, _ := g.Log("%s", 1)
	require.Equal(t, "pre(lint-fix)", subjects[0])

	// Op edits one file, creates another; user edits a third concurrently.
	require.NoError(t, op.Record("Note.md"))
	write(t, v.Path, "Note.md", "fixed content\n")
	require.NoError(t, op.Record("Created.md"))
	write(t, v.Path, "Created.md", "created by op\n")
	write(t, v.Path, "Concurrent.md", "user typed this during the op\n")
	require.NoError(t, op.End("2 file(s)"))

	undoOp, err := Undo(v, "last", t0.Add(time.Minute))
	require.NoError(t, err)
	require.Equal(t, []string{"Created.md", "Note.md"}, undoOp.Files)

	got, err := os.ReadFile(filepath.Join(v.Path, "Note.md"))
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(got), "op edit reverted byte-identically")

	_, statErr := os.Stat(filepath.Join(v.Path, "Created.md"))
	require.True(t, os.IsNotExist(statErr), "op-created file deleted")

	got, err = os.ReadFile(filepath.Join(v.Path, "Concurrent.md"))
	require.NoError(t, err)
	require.Equal(t, "user typed this during the op\n", string(got), "concurrent user edit survives")

	// Undo is itself an op: undoing it restores the fix.
	_, err = Undo(v, undoOp.ID, t0.Add(2*time.Minute))
	require.NoError(t, err)
	got, _ = os.ReadFile(filepath.Join(v.Path, "Note.md"))
	require.Equal(t, "fixed content\n", string(got))
	got, _ = os.ReadFile(filepath.Join(v.Path, "Created.md"))
	require.Equal(t, "created by op\n", string(got))
}

func TestUndoUnknownOp(t *testing.T) {
	v := newVault(t)
	_, err := Undo(v, "nope", t0)
	require.ErrorContains(t, err, "unknown operation")
	_, err = Undo(v, "last", t0)
	require.ErrorContains(t, err, "no operations recorded")
}

func TestHistoryListsOps(t *testing.T) {
	v := newVault(t)
	write(t, v.Path, "A.md", "a")
	Take(v, t0)

	op, err := Begin(v, "lint-fix", t0.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, op.Record("A.md"))
	write(t, v.Path, "A.md", "fixed")
	require.NoError(t, op.End("1 file(s)"))

	entries, err := History(v, 10)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	require.True(t, strings.HasPrefix(entries[0].Subject, "lint-fix:"))
	require.Equal(t, op.ID, entries[0].OpID, "trailer round-trips through git log")
}

func TestHostnameSanitized(t *testing.T) {
	require.Regexp(t, `^[a-z0-9-]+$`, Hostname())
}
