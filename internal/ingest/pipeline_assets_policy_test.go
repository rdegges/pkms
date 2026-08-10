package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/snapshot"
)

// opIDContaining finds the op whose journal mentions the given note name.
func opIDContaining(t *testing.T, vaultName, needle string) string {
	t.Helper()
	opsDir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pkms", "ops", vaultName)
	entries, err := os.ReadDir(opsDir)
	require.NoError(t, err)
	for _, e := range entries {
		raw, rerr := os.ReadFile(filepath.Join(opsDir, e.Name()))
		require.NoError(t, rerr)
		if bytes.Contains(raw, []byte(needle)) {
			return strings.TrimSuffix(e.Name(), ".json")
		}
	}
	t.Fatalf("no op journal mentions %q", needle)
	return ""
}

// SPEC §31.5: "new in-vault asset paths are appended to the op journal ...
// so `pkms undo` removes a note and its attachments together." The existing
// suite only covers the SHARED case, where the asset survives; this is the
// ordinary case, and it is the one that leaves litter in the vault if the
// journaling is wrong.
func TestUndoRemovesNoteAndItsUnsharedAsset(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("solely owned attachment")

	res, err := r.RunPush(context.Background(), assetRecord(1, "solo.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	notePath := filepath.Join(v.Path, res.Notes[0])
	assetPath := filepath.Join(v.Path, "Attachments", "solo.bin")
	require.FileExists(t, notePath)
	require.FileExists(t, assetPath)

	_, err = snapshot.Undo(v, opIDContaining(t, v.Name, "Asset 1.md"), testNow)
	require.NoError(t, err)

	require.NoFileExists(t, notePath, "the note is reverted")
	require.NoFileExists(t, assetPath,
		"an unshared attachment is reverted with its note (SPEC §31.5) — otherwise undo leaves orphans")
}

// A reused (not newly stored) asset is never journaled, so undoing the note
// that reused it must not touch it.
func TestUndoOfReusingNoteLeavesTheOriginalAsset(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("first note owns these bytes")

	_, err := r.RunPush(context.Background(), assetRecord(1, "owned.bin", body))
	require.NoError(t, err)
	_, err = r.RunPush(context.Background(), assetRecord(2, "owned.bin", body))
	require.NoError(t, err)

	// Undo the SECOND op (the reuser).
	_, err = snapshot.Undo(v, opIDContaining(t, v.Name, "Asset 2.md"), testNow)
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(v.Path, "Inbox", "Asset 2.md"))
	require.FileExists(t, filepath.Join(v.Path, "Attachments", "owned.bin"),
		"the reused asset belongs to note 1 and must survive")
	require.FileExists(t, filepath.Join(v.Path, "Inbox", "Asset 1.md"))
}

// SPEC §31.5: "Asset-store IO failure is an execution error (§17 exit 2, run
// aborts) — never a quarantine; quarantine means 'this record is bad', not
// 'this machine is bad'."
func TestAssetStoreFailureIsAnExecutionErrorNotAQuarantine(t *testing.T) {
	r, v := testRunner(t)
	rec := assetRecord(1, "boom.bin", []byte("payload"))
	sentinel := errors.New("input/output error")
	rec.Assets[0].Open = func() (io.ReadCloser, error) { return nil, sentinel }

	res, err := r.RunPush(context.Background(), rec)
	require.Error(t, err, "a store failure aborts the run")
	require.ErrorIs(t, err, sentinel)
	require.ErrorContains(t, err, "boom.bin", "the error names the asset")
	require.Equal(t, 0, res.Quarantined, "a bad machine is never a bad record")
	require.Equal(t, 0, res.New)

	// Nothing half-written landed in the vault.
	require.NoFileExists(t, filepath.Join(v.Path, "Attachments", "boom.bin"))
	notes, err := filepath.Glob(filepath.Join(v.Path, "Inbox", "*.md"))
	require.NoError(t, err)
	require.Empty(t, notes, "no note is written when its assets could not be stored")
}

// A record carrying several assets whose SECOND store fails must not leave
// the first one behind: the whole emit is abandoned (SPEC §31.5 cleanup).
func TestPartialAssetStoreFailureCleansUpEarlierAssets(t *testing.T) {
	r, v := testRunner(t)
	rec := assetRecord(1, "good.bin", []byte("stored fine"))
	badBody := []byte("never lands")
	badSum := sha256.Sum256(badBody)
	sentinel := errors.New("disk full")
	rec.Assets = append(rec.Assets, Asset{
		Filename: "bad.bin",
		SHA256:   hex.EncodeToString(badSum[:]),
		Size:     int64(len(badBody)),
		Open:     func() (io.ReadCloser, error) { return nil, sentinel },
	})

	_, err := r.RunPush(context.Background(), rec)
	require.ErrorIs(t, err, sentinel)
	require.NoFileExists(t, filepath.Join(v.Path, "Attachments", "good.bin"),
		"an asset stored earlier in the same emit is cleaned up when a later one fails")
	require.NoFileExists(t, filepath.Join(v.Path, "Attachments", "bad.bin"))
}

// The `## Attachments` section labels external links with the SANITIZED
// stored basename, so a hostile emitter filename cannot smuggle markdown or
// wikilink markup into the body (SPEC §28.9 posture).
func TestAttachmentsSectionNeutralizesHostileNames(t *testing.T) {
	r, v := testRunner(t)
	r.Vault.Assets.ThresholdBytes = 1 << 20 // keep it in-vault
	body := []byte("payload")
	rec := assetRecord(1, "../../evil [[Secret]] (x).png", body)

	res, err := r.RunPush(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	s := string(note)
	require.Contains(t, s, "## Attachments")
	require.NotContains(t, s, "[[Secret]]", "the hostile wikilink must not survive into the body")
	require.NotContains(t, s, "../..", "no traversal text is echoed as a path")

	// Whatever path was rendered must be a real file under Attachments/.
	entries, err := os.ReadDir(filepath.Join(v.Path, "Attachments"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Contains(t, s, entries[0].Name(), "the embed points at the file that actually landed")
}

// The ledger is the single machine-readable record (SPEC §31.4): one entry
// per carried asset, in order, matching what was stored.
func TestAssetsLedgerMatchesStoredPaths(t *testing.T) {
	r, v := testRunner(t)
	rec := assetRecord(1, "one.bin", []byte("first"))
	second := []byte("second")
	sum := sha256.Sum256(second)
	rec.Assets = append(rec.Assets, Asset{
		Filename: "two.bin",
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(second)),
		Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(second)), nil },
	})

	res, err := r.RunPush(context.Background(), rec)
	require.NoError(t, err)

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	s := string(note)
	require.Contains(t, s, "Attachments/one.bin")
	require.Contains(t, s, "Attachments/two.bin")
	require.Contains(t, s, "![[Attachments/one.bin]]")
	require.Contains(t, s, "![[Attachments/two.bin]]")
	require.FileExists(t, filepath.Join(v.Path, "Attachments", "one.bin"))
	require.FileExists(t, filepath.Join(v.Path, "Attachments", "two.bin"))
}

// An asset-less record must gain neither the ledger nor the section — the
// uniform `## Attachments` heading is only for records that carried assets.
func TestNoAttachmentsSectionWithoutAssets(t *testing.T) {
	r, v := testRunner(t)
	rec := Record{
		NaturalKey: "https://example.com/plain",
		NoteType:   "clip",
		Fields: map[string]any{
			"title": "Plain", "source": "https://example.com/plain",
			"created": "2026-08-03", "tags": []any{"clip"},
		},
		Body: "body text\n",
	}
	res, err := r.RunPush(context.Background(), rec)
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	require.NotContains(t, string(note), "## Attachments")
	require.NotContains(t, string(note), "assets:")
}

// Idempotent reuse must heal the §31.5 crash window: an asset left on disk by
// a run that died before the ack is adopted, not duplicated, on the re-run.
func TestReRunAdoptsOrphanedAssetInsteadOfDuplicating(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("orphaned by a crash")

	// Simulate the crash residue: the attachment exists, no note references it.
	dir := filepath.Join(v.Path, "Attachments")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "crash.bin"), body, 0o644))

	res, err := r.RunPush(context.Background(), assetRecord(1, "crash.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "the orphan is adopted, not duplicated as 'crash 2.bin'")

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	require.Contains(t, string(note), "Attachments/crash.bin")

	// The adopted asset was NOT newly stored, so it is not journaled and an
	// undo of this note leaves it on disk.
	_, err = snapshot.Undo(v, opIDContaining(t, v.Name, "Asset 1.md"), testNow)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(dir, "crash.bin"))
}
