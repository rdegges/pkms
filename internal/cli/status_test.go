package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/lock"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/profile"
)

// Decode the public fields independently of the implementation structs.
type statusPayload struct {
	Vault string `json:"vault"`
	Inbox struct {
		Status  string  `json:"status"`
		Count   *int    `json:"count"`
		Undated *int    `json:"undated_count"`
		Oldest  *string `json:"oldest_created_at"`
	} `json:"inbox"`
	Ingest struct {
		LastSuccess *string `json:"last_success_at"`
	} `json:"ingest"`
	Snapshot struct {
		Status     string  `json:"status"`
		Commit     *string `json:"latest_commit"`
		CommitAt   *string `json:"latest_commit_at"`
		Dirty      *bool   `json:"dirty"`
		InProgress *bool   `json:"operation_in_progress"`
		LastRun    *string `json:"last_run_at"`
	} `json:"snapshot"`
	Quarantine struct {
		Status string `json:"status"`
		Files  *int   `json:"files"`
	} `json:"quarantine"`
	Checks []checkResult `json:"checks"`
}

func readStatus(t *testing.T, args ...string) (statusPayload, string, error) {
	t.Helper()
	out, err := runCLI(t, append([]string{"status", "--json"}, args...)...)
	var report statusPayload
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	return report, out, err
}

func statusVault(t *testing.T, prof string) string {
	t.Helper()
	testEnv(t)
	dir := filepath.Join(t.TempDir(), "vault")
	out, err := runCLI(t, "init", "--path", dir, "--name", "test", "--profile", prof)
	require.NoError(t, err, out)
	return dir
}

func writeStatusFile(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
}

func TestStatusCountsCaptureFoldersAndSourceDates(t *testing.T) {
	for _, name := range []string{"para", "rdegges"} {
		t.Run(name, func(t *testing.T) {
			dir := statusVault(t, name)
			prof, err := profile.Load(name)
			require.NoError(t, err)
			folder := prof.Type(prof.Ingest.Clip).Folder
			writeStatusFile(t, dir, folder+"/old.md", "---\ncreated: 2001-02-03T04:05:06-08:00\n---\nold mail\n")
			writeStatusFile(t, dir, folder+"/asset.md", "---\ncreated: 2002-03-04\nsha256: abc\n---\nasset\n")
			writeStatusFile(t, dir, folder+"/invalid.md", "---\ncreated: [\n---\ninvalid YAML\n")
			writeStatusFile(t, dir, folder+"/undated.md", "no date\n")
			writeStatusFile(t, dir, folder+"/future.md", "---\ncreated: 2099-01-01T00:00:00Z\n---\nfuture source date\n")
			writeStatusFile(t, dir, "Resources/outside.md", "---\ncreated: 1900-01-01\n---\noutside\n")
			if name == "rdegges" {
				writeStatusFile(t, dir, "Resources/Clips/Processed/done.md", "---\ncreated: 1900-01-01\n---\nprocessed\n")
			}
			r, _, err := readStatus(t)
			require.NoError(t, err)
			require.Equal(t, "ok", r.Inbox.Status)
			require.NotNil(t, r.Inbox.Count)
			require.Equal(t, 5, *r.Inbox.Count)
			require.Equal(t, 2, *r.Inbox.Undated)
			require.Equal(t, "2001-02-03T12:05:06Z", *r.Inbox.Oldest)
			require.Nil(t, r.Ingest.LastSuccess)
			require.Nil(t, r.Snapshot.LastRun)
			require.True(t, *r.Snapshot.Dirty)
		})
	}
}

func TestStatusEmptyAndFreshnessRemainUnknown(t *testing.T) {
	dir := statusVault(t, "para")
	for _, body := range []string{
		"{\"v\":1}\n{\"op\":\"ack\",\"ts\":\"2099-01-01T00:00:00Z\"}\n",
		"{\"v\":1}\n{\"op\":\"ack\",\"k\":\"compacted\"}\n",
	} {
		writeStatusFile(t, paths.StateDir(), "state/test/adhoc.ndjson", body)
		r, out, err := readStatus(t)
		require.NoError(t, err)
		require.Zero(t, *r.Inbox.Count)
		require.Zero(t, *r.Inbox.Undated)
		require.Nil(t, r.Inbox.Oldest)
		require.Nil(t, r.Ingest.LastSuccess)
		require.Nil(t, r.Snapshot.LastRun)
		require.Contains(t, out, `"last_success_at": null`)
		require.NotNil(t, r.Snapshot.Commit)
		require.NotNil(t, r.Snapshot.CommitAt)
		require.False(t, *r.Snapshot.Dirty)
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	require.NoError(t, err)
}

func TestStatusQuarantineIncludesUnconfiguredSourcesAndReportsFailure(t *testing.T) {
	statusVault(t, "para")
	q := paths.StateDir("failed", "test")
	r, _, err := readStatus(t)
	require.NoError(t, err)
	require.Zero(t, *r.Quarantine.Files)
	writeStatusFile(t, q, "adhoc/one.json", "{}")
	writeStatusFile(t, q, "retired/two.json", "{}")
	r, _, err = readStatus(t)
	require.ErrorIs(t, err, errFindings)
	require.Equal(t, 2, *r.Quarantine.Files)
	require.NoError(t, os.Symlink("/does-not-exist", filepath.Join(q, "uninspected-symlink")))
	r, _, err = readStatus(t)
	require.ErrorIs(t, err, errFindings)
	require.Equal(t, "unknown", r.Quarantine.Status)
	require.Nil(t, r.Quarantine.Files, "uninspected entries cannot prove a complete quarantine count")
	require.NoError(t, os.RemoveAll(q))
	writeStatusFile(t, filepath.Dir(q), filepath.Base(q), "not a directory")
	r, _, err = readStatus(t)
	require.Error(t, err)
	require.False(t, errors.Is(err, errFindings))
	require.Equal(t, "error", r.Quarantine.Status)
	require.Nil(t, r.Quarantine.Files)
}

func TestStatusInspectionFailuresStayUnknown(t *testing.T) {
	dir := statusVault(t, "para")
	require.NoError(t, os.RemoveAll(dir))
	r, _, err := readStatus(t)
	require.Error(t, err)
	require.False(t, errors.Is(err, errFindings))
	require.Equal(t, "error", r.Inbox.Status)
	require.Nil(t, r.Inbox.Count)
	require.Nil(t, r.Snapshot.Dirty)
}

func TestStatusRejectsFileInsteadOfVaultAndBrokenGitMetadata(t *testing.T) {
	dir := statusVault(t, "para")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".git")))
	writeStatusFile(t, dir, ".git", "not a gitdir pointer\n")
	r, _, err := readStatus(t)
	require.Error(t, err)
	require.False(t, errors.Is(err, errFindings))
	require.Equal(t, "error", r.Snapshot.Status)
	require.Nil(t, r.Snapshot.Commit)
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.WriteFile(dir, []byte("not a vault directory"), 0o644))
	r, _, err = readStatus(t)
	require.Error(t, err)
	require.Equal(t, "error", r.Inbox.Status)
	require.Nil(t, r.Inbox.Count)
}

func TestStatusEmptyRepositoryHasNoRecoveryPoint(t *testing.T) {
	dir := statusVault(t, "para")
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".git")))
	require.NoError(t, gitx.Init(dir))
	r, _, err := readStatus(t)
	require.ErrorIs(t, err, errFindings)
	require.Equal(t, "unavailable", r.Snapshot.Status)
	require.Nil(t, r.Snapshot.Commit)
	require.Nil(t, r.Snapshot.CommitAt)
}

func TestStatusDoesNotCreateMissingStateOrProbeSourceSecrets(t *testing.T) {
	statusVault(t, "para")
	cfg, err := config.Load("")
	require.NoError(t, err)
	f, err := os.OpenFile(cfg.Path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString("\n[[vaults.ingesters]]\ntype = \"imap\"\nname = \"mail\"\npassword_cmd = [\"sentinel-secret-command-must-not-run\"]\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, os.RemoveAll(paths.StateDir()))
	_, out, err := readStatus(t)
	require.NoError(t, err)
	require.NotContains(t, out, "sentinel-secret")
	_, err = os.Stat(paths.StateDir())
	require.True(t, os.IsNotExist(err), "status must not create state directories")
}

func TestStatusTextEscapesTerminalControls(t *testing.T) {
	dir := statusVault(t, "para")
	body := fmt.Sprintf("version = 1\n[[vaults]]\nname = \"test\"\npath = %q\nprofile = \"missing\\u001b[31mprofile\\nforged line\"\n", dir)
	require.NoError(t, os.WriteFile(paths.ConfigFile(), []byte(body), 0o644))
	out, err := runCLI(t, "status")
	require.Error(t, err)
	require.NotContains(t, out, "\x1b")
	require.NotContains(t, out, "\nforged line")
	require.Contains(t, out, `\x1b`)
}

func TestStatusMergeAndMissingRecoveryPoint(t *testing.T) {
	dir := statusVault(t, "para")
	writeStatusFile(t, dir, ".git/MERGE_HEAD", strings.Repeat("0", 40)+"\n")
	r, _, err := readStatus(t)
	require.ErrorIs(t, err, errFindings)
	require.True(t, *r.Snapshot.InProgress)
	require.NoError(t, os.RemoveAll(filepath.Join(dir, ".git")))
	r, _, err = readStatus(t)
	require.ErrorIs(t, err, errFindings)
	require.Equal(t, "unavailable", r.Snapshot.Status)
	require.Nil(t, r.Snapshot.Commit)
	require.Nil(t, r.Snapshot.Dirty)
}

func TestStatusSelectsOneVaultAndValidatesLintConfiguration(t *testing.T) {
	statusVault(t, "para")
	other := filepath.Join(t.TempDir(), "other")
	out, err := runCLI(t, "init", "--path", other, "--name", "other")
	require.NoError(t, err, out)
	_, err = runCLI(t, "status", "--json")
	require.Error(t, err)
	r, _, err := readStatus(t, "--vault", "test")
	require.NoError(t, err)
	require.Equal(t, "test", r.Vault)
	cfg, err := config.Load("")
	require.NoError(t, err)
	body := fmt.Sprintf("version = 1\n[[vaults]]\nname = \"test\"\npath = %q\nprofile = \"para\"\n[vaults.lint.not-a-rule]\nenabled = true\n", cfg.Vaults[0].Path)
	require.NoError(t, os.WriteFile(cfg.Path, []byte(body), 0o644))
	r, _, err = readStatus(t, "--vault", "test")
	require.Error(t, err)
	require.False(t, errors.Is(err, errFindings))
	found := false
	for _, check := range r.Checks {
		if check.Name == "lint-config" {
			found = check.Status == "fail"
		}
	}
	require.True(t, found)
}

func TestStatusDoesNotMutateOrContendWithIngest(t *testing.T) {
	dir := statusVault(t, "para")
	l, err := lock.Acquire(paths.StateDir("locks", "test.lock"))
	require.NoError(t, err)
	defer func() { require.NoError(t, l.Release()) }()
	writeStatusFile(t, paths.StateDir(), "state/test/adhoc.ndjson", "malformed ledger, deliberately unread by status")
	old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, os.Chtimes(paths.StateDir("locks", "test.lock"), old, old))
	before := statusTree(t, filepath.Dir(paths.StateDir()))
	vaultBefore := statusTree(t, dir)
	_, out, err := readStatus(t)
	require.NoError(t, err)
	require.NotContains(t, out, "stale lock")
	require.Equal(t, before, statusTree(t, filepath.Dir(paths.StateDir())))
	require.Equal(t, vaultBefore, statusTree(t, dir))
}

func TestStatusTemplateCaptureFolderIsExplicitlyUnknown(t *testing.T) {
	dir := statusVault(t, "para")
	prof, err := profile.Load("para")
	require.NoError(t, err)
	ejected := filepath.Join(t.TempDir(), "profile")
	require.NoError(t, prof.Eject(ejected))
	p := filepath.Join(ejected, "profile.toml")
	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, []byte(strings.ReplaceAll(string(raw), `folder = "_Inbox"`, `folder = "_Inbox/{{.category}}"`)), 0o644))
	cfg, err := config.Load("")
	require.NoError(t, err)
	body := fmt.Sprintf("version = 1\n[[vaults]]\nname = \"test\"\npath = %q\nprofile = %q\n", dir, ejected)
	require.NoError(t, os.WriteFile(cfg.Path, []byte(body), 0o644))
	writeStatusFile(t, dir, "_Inbox/mail.md", "a note")
	r, _, err := readStatus(t)
	require.NoError(t, err)
	require.Equal(t, "unknown", r.Inbox.Status)
	require.Nil(t, r.Inbox.Count)
}

func statusTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			files[p] = "directory"
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		files[p] = string(raw) + st.ModTime().String()
		return nil
	})
	require.NoError(t, err)
	return files
}

func statusTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func TestStatusDoesNotExecuteGitFilters(t *testing.T) {
	for _, filter := range []string{"clean", "process"} {
		t.Run(filter, func(t *testing.T) {
			dir := statusVault(t, "para")
			writeStatusFile(t, dir, ".gitattributes", "filtered.md filter=sentinel\n")
			writeStatusFile(t, dir, "filtered.md", "unchanged content\n")
			g := gitx.Git{Dir: dir}
			require.NoError(t, g.AddAll())
			_, err := g.Commit("seed filter input")
			require.NoError(t, err)
			probeDir := t.TempDir()
			sentinel := filepath.Join(probeDir, "sentinel")
			script := filepath.Join(probeDir, "filter.sh")
			body := fmt.Sprintf("#!/bin/sh\n: > '%s'\ncat\n", sentinel)
			if filter == "process" {
				body = fmt.Sprintf("#!/bin/sh\n: > '%s'\nexit 1\n", sentinel)
			}
			require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
			statusTestGit(t, dir, "config", "--local", "filter.sentinel."+filter, script)
			old := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
			require.NoError(t, os.Chtimes(filepath.Join(dir, "filtered.md"), old, old))
			r, out, err := readStatus(t)
			_, statErr := os.Stat(sentinel)
			require.True(t, os.IsNotExist(statErr), "status executed the configured %s filter", filter)
			require.NoError(t, err)
			require.Nil(t, r.Snapshot.Dirty)
			require.NotNil(t, r.Snapshot.Commit)
			require.NotContains(t, out, script, "filter command values must not be exposed")
		})
	}
}

func TestStatusDoesNotFetchPromisorObjects(t *testing.T) {
	dir := statusVault(t, "para")
	r, _, err := readStatus(t)
	require.NoError(t, err)
	probeDir := t.TempDir()
	sentinel := filepath.Join(probeDir, "sentinel")
	helper := filepath.Join(probeDir, "git-remote-statusprobe")
	require.NoError(t, os.WriteFile(helper, []byte(fmt.Sprintf("#!/bin/sh\n: > '%s'\nexit 1\n", sentinel)), 0o755))
	t.Setenv("PATH", probeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	statusTestGit(t, dir, "config", "--local", "remote.origin.url", "statusprobe::fixture")
	statusTestGit(t, dir, "config", "--local", "remote.origin.promisor", "true")
	sha := *r.Snapshot.Commit
	require.NoError(t, os.Remove(filepath.Join(dir, ".git", "objects", sha[:2], sha[2:])))
	r, _, err = readStatus(t)
	_, statErr := os.Stat(sentinel)
	require.True(t, os.IsNotExist(statErr), "status attempted a lazy object fetch")
	require.Error(t, err)
	require.Nil(t, r.Snapshot.Commit)
	require.Nil(t, r.Snapshot.Dirty)
}

func TestStatusDoesNotExecuteSignatureHelpers(t *testing.T) {
	dir := statusVault(t, "para")
	tree := statusTestGit(t, dir, "rev-parse", "HEAD^{tree}")
	probeDir := t.TempDir()
	sentinel := filepath.Join(probeDir, "sentinel")
	helper := filepath.Join(probeDir, "gpg-probe")
	require.NoError(t, os.WriteFile(helper, []byte(fmt.Sprintf("#!/bin/sh\n: > '%s'\nexit 1\n", sentinel)), 0o755))
	commitFile := filepath.Join(probeDir, "commit")
	commit := fmt.Sprintf("tree %s\nauthor Fixture <fixture@example.test> 1700000000 +0000\ncommitter Fixture <fixture@example.test> 1700000000 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n fixture\n -----END PGP SIGNATURE-----\n\nSigned fixture\n", tree)
	require.NoError(t, os.WriteFile(commitFile, []byte(commit), 0o644))
	sha := statusTestGit(t, dir, "hash-object", "-t", "commit", "-w", commitFile)
	statusTestGit(t, dir, "update-ref", "HEAD", sha)
	statusTestGit(t, dir, "config", "--local", "log.showSignature", "true")
	statusTestGit(t, dir, "config", "--local", "gpg.program", helper)
	r, _, err := readStatus(t)
	require.NoError(t, err)
	require.Equal(t, sha, *r.Snapshot.Commit)
	_, err = os.Stat(sentinel)
	require.True(t, os.IsNotExist(err), "status executed a signature helper")
}

func TestStatusSubmoduleChangesStayUnknown(t *testing.T) {
	dir := statusVault(t, "para")
	sha := statusTestGit(t, dir, "rev-parse", "HEAD")
	statusTestGit(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+sha+",nested")
	r, _, err := readStatus(t)
	require.NoError(t, err)
	require.Nil(t, r.Snapshot.Dirty)
	out, err := runCLI(t, "status")
	require.NoError(t, err)
	require.Contains(t, out, "unsnapshotted changes: unknown")
	require.Contains(t, out, "submodules")
}

func TestStatusRejectsNonCommitAndCorruptHeads(t *testing.T) {
	for _, kind := range []string{"tree", "broken-ref"} {
		t.Run(kind, func(t *testing.T) {
			dir := statusVault(t, "para")
			if kind == "tree" {
				tree := statusTestGit(t, dir, "rev-parse", "HEAD^{tree}")
				writeStatusFile(t, dir, ".git/HEAD", tree+"\n")
			} else {
				ref := statusTestGit(t, dir, "symbolic-ref", "HEAD")
				writeStatusFile(t, dir, ".git/"+ref, "broken ref\n")
			}
			r, _, err := readStatus(t)
			require.Error(t, err)
			require.False(t, errors.Is(err, errFindings), "corrupt recovery metadata must be an inspection error")
			require.Equal(t, "error", r.Snapshot.Status)
			require.Nil(t, r.Snapshot.Commit)
			require.Nil(t, r.Snapshot.CommitAt)
		})
	}
}

func TestStatusCaptureDirectoriesMustBeRealDirectories(t *testing.T) {
	for _, kind := range []string{"file", "symlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := statusVault(t, "para")
			capture := filepath.Join(dir, "_Inbox")
			require.NoError(t, os.RemoveAll(capture))
			switch kind {
			case "file":
				require.NoError(t, os.WriteFile(capture, []byte("not a folder"), 0o644))
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), capture))
			}
			r, _, err := readStatus(t)
			if kind == "missing" {
				require.NoError(t, err)
				require.Zero(t, *r.Inbox.Count)
			} else {
				require.Error(t, err)
				require.Equal(t, "error", r.Inbox.Status)
				require.Nil(t, r.Inbox.Count)
			}
		})
	}
}

func TestStatusQuarantineDoesNotFollowAncestorSymlinks(t *testing.T) {
	for _, ancestor := range []string{"", "failed"} {
		t.Run(ancestor, func(t *testing.T) {
			statusVault(t, "para")
			p := paths.StateDir(ancestor)
			require.NoError(t, os.RemoveAll(p))
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "missing"), p))
			r, _, err := readStatus(t)
			require.Error(t, err)
			require.Equal(t, "error", r.Quarantine.Status)
			require.Nil(t, r.Quarantine.Files)
		})
	}
}

func TestStatusRejectsUnknownEnabledSourceType(t *testing.T) {
	dir := statusVault(t, "para")
	body := fmt.Sprintf("version = 1\n[[vaults]]\nname = \"test\"\npath = %q\nprofile = \"para\"\n[[vaults.ingesters]]\ntype = \"impa\"\nname = \"mail\"\n", dir)
	require.NoError(t, os.WriteFile(paths.ConfigFile(), []byte(body), 0o644))
	r, _, err := readStatus(t)
	require.Error(t, err)
	require.False(t, errors.Is(err, errFindings))
	found := false
	for _, c := range r.Checks {
		if c.Name == "ingest-config" && c.Status == "fail" {
			found = true
		}
	}
	require.True(t, found)
}

type statusFailedWriter struct{ err error }

func (w statusFailedWriter) Write([]byte) (int, error) { return 0, w.err }

func TestStatusPropagatesHumanOutputFailures(t *testing.T) {
	statusVault(t, "para")
	for _, writerErr := range []error{io.ErrClosedPipe, nil} {
		root := newRootCmd()
		root.SetArgs([]string{"status"})
		root.SetOut(statusFailedWriter{err: writerErr})
		err := root.Execute()
		if writerErr == nil {
			require.ErrorIs(t, err, io.ErrShortWrite)
		} else {
			require.ErrorIs(t, err, writerErr)
		}
	}
}
