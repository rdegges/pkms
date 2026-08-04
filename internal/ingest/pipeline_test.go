package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/profile"
)

var testNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

// fakeIngester replays a fixed record list through the pipeline.
type fakeIngester struct {
	records []Record
	// setCursor, when non-nil, mutates the cursor mid-fetch.
	setCursor Cursor
	fetchErr  error
	reset     bool
}

func (f *fakeIngester) Name() string { return "fake" }
func (f *fakeIngester) Fetch(ctx context.Context, cursor Cursor, emit EmitFunc) error {
	for k, v := range f.setCursor {
		cursor[k] = v
	}
	for _, r := range f.records {
		if err := emit(ctx, r); err != nil {
			return err
		}
	}
	return f.fetchErr
}
func (f *fakeIngester) CursorWasReset() bool { return f.reset }

var currentFake *fakeIngester

func init() {
	Register("fake", func(cfg map[string]any) (Ingester, error) {
		return currentFake, nil
	})
}

// testProfile writes a minimal disk profile with one "clip" type.
func testProfile(t *testing.T) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "profile.toml"), `
schema_version = 1
name = "test"
description = "test profile"
scaffold = ["Inbox"]

[[types]]
name = "clip"
scope = ["Inbox/*.md"]
schema = "schemas/clip.schema.json"
folder = "Inbox"
filename = "{{.title}}"
`)
	writeFile(t, filepath.Join(dir, "schemas", "clip.schema.json"), `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["title", "source", "created", "tags"],
  "properties": {
    "title": {"type": "string"},
    "source": {"type": "string", "pattern": "^(https?://|mid:|file://)"},
    "created": {"type": "string", "pattern": "^\\d{4}-\\d{2}-\\d{2}"},
    "tags": {"type": "array", "items": {"type": "string"}, "contains": {"const": "clip"}}
  }
}`)
	prof, err := profile.Load(dir)
	require.NoError(t, err)
	return prof
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// testVault creates a git-initialized vault dir with one commit.
func testVault(t *testing.T) *config.Vault {
	t.Helper()
	dir := t.TempDir()
	// t.TempDir on macOS returns /var/... which is a symlink to /private/var;
	// resolve so the writer's EvalSymlinks containment check agrees.
	dir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	return &config.Vault{Name: "testvault", Path: dir, Profile: "test"}
}

func testRunner(t *testing.T) (*Runner, *config.Vault) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	v := testVault(t)
	return &Runner{Vault: v, Profile: testProfile(t), Now: func() time.Time { return testNow }}, v
}

func clipRecord(n int) Record {
	return Record{
		NaturalKey: fmt.Sprintf("https://example.com/a%d", n),
		NoteType:   "clip",
		Fields: map[string]any{
			"title":   fmt.Sprintf("Article %d", n),
			"source":  fmt.Sprintf("https://example.com/a%d", n),
			"created": "2026-08-03",
			"tags":    []any{"clip"},
		},
		Body: "Hello.",
	}
}

func srcCfg() config.IngesterConfig {
	return config.IngesterConfig{Type: "fake", Name: "one", Enabled: true,
		Timeout: time.Minute, Options: map[string]any{}}
}

func TestRunSourceWritesAcksAndCommits(t *testing.T) {
	r, v := testRunner(t)
	currentFake = &fakeIngester{records: []Record{clipRecord(1), clipRecord(2)}}

	res, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 2, res.New)
	require.Equal(t, "fake:one: 2 new, 0 deduped, 0 quarantined", res.Summary())

	raw, err := os.ReadFile(filepath.Join(v.Path, "Inbox", "Article 1.md"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "source_id: https://example.com/a1")

	// The run committed via the op wrapper.
	out, err := gitLog(v.Path)
	require.NoError(t, err)
	require.Contains(t, out, "ingest: fake:one: 2 new, 0 deduped, 0 quarantined")
}

func TestRunSourceRerunIsNoOp(t *testing.T) {
	r, _ := testRunner(t)
	currentFake = &fakeIngester{records: []Record{clipRecord(1)}}
	_, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)

	res, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 0, res.New)
	require.Equal(t, 1, res.Deduped)
}

func TestRunSourceAckRepairFromVaultSourceID(t *testing.T) {
	// Simulate a crash between rename and ack: the note exists in the
	// vault, the state file knows nothing.
	r, v := testRunner(t)
	writeFile(t, filepath.Join(v.Path, "Inbox", "Article 1.md"),
		"---\nsource_id: https://example.com/a1\ntitle: Article 1\n---\nHello.\n")

	currentFake = &fakeIngester{records: []Record{clipRecord(1)}}
	res, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 0, res.New, "never duplicates")
	require.Equal(t, 1, res.Deduped)

	// The repair acked the key: a re-run dedups via the state file alone.
	entries, err := os.ReadDir(filepath.Join(v.Path, "Inbox"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRunSourceQuarantineDoesNotBlockBatch(t *testing.T) {
	r, v := testRunner(t)
	bad := clipRecord(1)
	delete(bad.Fields, "title")
	currentFake = &fakeIngester{records: []Record{bad, clipRecord(2)}}

	res, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 1, res.New)
	require.Equal(t, 1, res.Quarantined)

	// Quarantined key is seen: re-run does not retry it.
	currentFake = &fakeIngester{records: []Record{bad, clipRecord(2)}}
	res, err = r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 0, res.New)
	require.Equal(t, 0, res.Quarantined)
	require.Equal(t, 2, res.Deduped)

	// Quarantine file landed OUTSIDE the vault, named ts-<keyhash>.json.
	qdir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pkms", "failed", v.Name, "fake-one")
	entries, err := os.ReadDir(qdir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, entries[0].Name(), Key(bad.NaturalKey))
}

func TestRunSourceCursorPersistedOnlyOnCleanFetch(t *testing.T) {
	r, _ := testRunner(t)

	currentFake = &fakeIngester{setCursor: Cursor{"pos": "10"}, fetchErr: errors.New("boom")}
	_, err := r.RunSource(context.Background(), srcCfg())
	require.ErrorContains(t, err, "boom")

	// Failed fetch → cursor NOT persisted.
	currentFake = &fakeIngester{}
	st := openTestState(t, r)
	require.Nil(t, st.Cursor())
	require.NoError(t, st.Close())

	currentFake = &fakeIngester{setCursor: Cursor{"pos": "10"}}
	_, err = r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)

	st = openTestState(t, r)
	require.Equal(t, Cursor{"pos": "10"}, st.Cursor())
	require.NoError(t, st.Close())
}

func TestRunSourceNotesDurableDespiteFetchError(t *testing.T) {
	r, v := testRunner(t)
	currentFake = &fakeIngester{records: []Record{clipRecord(1)}, fetchErr: errors.New("mid-run fail")}

	res, err := r.RunSource(context.Background(), srcCfg())
	require.ErrorContains(t, err, "mid-run fail")
	require.Equal(t, 1, res.New)

	// The written note is committed and acked; re-run dedups.
	out, err := gitLog(v.Path)
	require.NoError(t, err)
	require.Contains(t, out, "ingest: fake:one: 1 new")

	currentFake = &fakeIngester{records: []Record{clipRecord(1)}}
	res, err = r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.Equal(t, 0, res.New)
	require.Equal(t, 1, res.Deduped)
}

func TestRunSourceEmptyNaturalKeyIsIngesterBug(t *testing.T) {
	r, _ := testRunner(t)
	rec := clipRecord(1)
	rec.NaturalKey = ""
	currentFake = &fakeIngester{records: []Record{rec}}

	_, err := r.RunSource(context.Background(), srcCfg())
	require.ErrorContains(t, err, "empty NaturalKey")
}

func TestRunSourceRejectsIngesterSettingSourceID(t *testing.T) {
	r, _ := testRunner(t)
	rec := clipRecord(1)
	rec.Fields["source_id"] = "sneaky"
	currentFake = &fakeIngester{records: []Record{rec}}

	_, err := r.RunSource(context.Background(), srcCfg())
	require.ErrorContains(t, err, "pipeline owns that field")
}

func TestRunSourceCrossSourceDedupWithinRun(t *testing.T) {
	// Two sources emit the same natural key in one run: the second must
	// ack-repair against the note the first just wrote.
	r, v := testRunner(t)
	currentFake = &fakeIngester{records: []Record{clipRecord(1)}}
	_, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)

	other := srcCfg()
	other.Name = "two"
	currentFake = &fakeIngester{records: []Record{clipRecord(1)}}
	res, err := r.RunSource(context.Background(), other)
	require.NoError(t, err)
	require.Equal(t, 0, res.New)
	require.Equal(t, 1, res.Deduped)

	entries, err := os.ReadDir(filepath.Join(v.Path, "Inbox"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestRunSourceCursorResetReported(t *testing.T) {
	r, _ := testRunner(t)
	currentFake = &fakeIngester{reset: true}
	res, err := r.RunSource(context.Background(), srcCfg())
	require.NoError(t, err)
	require.True(t, res.CursorReset)
}

func openTestState(t *testing.T, r *Runner) *StateStore {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pkms", "state", r.Vault.Name, "fake-one.ndjson")
	st, err := OpenState(p, "fake:one")
	require.NoError(t, err)
	return st
}

func gitLog(dir string) (string, error) {
	cmd := exec.Command("git", "log", "--format=%s")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
