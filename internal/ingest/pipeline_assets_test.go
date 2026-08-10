package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/snapshot"
)

// assetRecord builds a valid asset-note record carrying one asset.
func assetRecord(n int, filename string, body []byte) Record {
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	return Record{
		NaturalKey: fmt.Sprintf("https://example.com/f%d", n),
		NoteType:   "asset",
		Fields: map[string]any{
			"title":   fmt.Sprintf("Asset %d", n),
			"source":  fmt.Sprintf("https://example.com/f%d", n),
			"created": "2026-08-03",
			"tags":    []any{"asset"},
			"mime":    "application/octet-stream",
			"size":    int64(len(body)),
			"sha256":  sha,
		},
		Assets: []Asset{{
			Filename: filename,
			SHA256:   sha,
			Size:     int64(len(body)),
			Open: func() (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader(body)), nil
			},
		}},
	}
}

func TestRunPushAssetStoresStampsAndLinks(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("tiny binary payload")

	res, err := r.RunPush(context.Background(), assetRecord(1, "payload.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	// The attachment landed in the vault.
	stored, err := os.ReadFile(filepath.Join(v.Path, "Attachments", "payload.bin"))
	require.NoError(t, err)
	require.Equal(t, body, stored)

	// The note stamps the assets: ledger and the ## Attachments section.
	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	require.Contains(t, string(note), "assets:")
	require.Contains(t, string(note), "Attachments/payload.bin")
	require.Contains(t, string(note), "## Attachments")
	require.Contains(t, string(note), "![[Attachments/payload.bin]]")

	// Both the note and its attachment are committed (op journal → git).
	log, err := gitLog(v.Path)
	require.NoError(t, err)
	require.Contains(t, log, "ingest: adhoc: 1 new")
}

func TestRunPushAssetRerunDedups(t *testing.T) {
	r, _ := testRunner(t)
	body := []byte("same bytes")

	res1, err := r.RunPush(context.Background(), assetRecord(1, "a.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res1.New)

	res2, err := r.RunPush(context.Background(), assetRecord(1, "a.bin", body))
	require.NoError(t, err)
	require.Equal(t, 0, res2.New)
	require.Equal(t, 1, res2.Deduped)
}

func TestQuarantineRemovesNewAssetKeepsReused(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("shared attachment bytes")

	// Note 1 stores the asset successfully.
	res, err := r.RunPush(context.Background(), assetRecord(1, "shared.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res.New)
	assetPath := filepath.Join(v.Path, "Attachments", "shared.bin")
	require.FileExists(t, assetPath)

	// Note 2 carries the SAME asset (reuse) but fails schema validation.
	bad := assetRecord(2, "shared.bin", body)
	delete(bad.Fields, "mime") // required by the asset schema
	res, err = r.RunPush(context.Background(), bad)
	require.NoError(t, err)
	require.Equal(t, 1, res.Quarantined)
	require.FileExists(t, assetPath, "reused asset survives its record's quarantine")

	// Note 3 carries a NEW asset and fails schema validation → cleanup.
	bad2 := assetRecord(3, "fresh.bin", []byte("brand new bytes"))
	delete(bad2.Fields, "mime")
	res, err = r.RunPush(context.Background(), bad2)
	require.NoError(t, err)
	require.Equal(t, 1, res.Quarantined)
	require.NoFileExists(t, filepath.Join(v.Path, "Attachments", "fresh.bin"),
		"an asset newly stored for a quarantined record is removed")
}

func TestUndoKeepsAssetSharedWithAnotherNote(t *testing.T) {
	r, v := testRunner(t)
	body := []byte("shared attachment bytes")

	// Op 1: note 1 + the attachment (journaled together).
	_, err := r.RunPush(context.Background(), assetRecord(1, "shared.bin", body))
	require.NoError(t, err)
	// Op 2: note 2 reuses the same attachment (not journaled — reused).
	_, err = r.RunPush(context.Background(), assetRecord(2, "shared.bin", body))
	require.NoError(t, err)

	// Undo op 1 (found by its journaled note — op IDs share a timestamp):
	// its note goes, but note 2 still references the asset.
	opsDir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "pkms", "ops", v.Name)
	ops, err := os.ReadDir(opsDir)
	require.NoError(t, err)
	op1 := ""
	for _, e := range ops {
		raw, rerr := os.ReadFile(filepath.Join(opsDir, e.Name()))
		require.NoError(t, rerr)
		if bytes.Contains(raw, []byte("Asset 1.md")) {
			op1 = e.Name()[:len(e.Name())-len(".json")]
		}
	}
	require.NotEmpty(t, op1)

	_, err = snapshot.Undo(v, op1, testNow)
	require.NoError(t, err)

	require.NoFileExists(t, filepath.Join(v.Path, "Inbox", "Asset 1.md"), "the op's note is reverted")
	require.FileExists(t, filepath.Join(v.Path, "Attachments", "shared.bin"),
		"an asset another note still references survives the undo (SPEC §31.5)")
	require.FileExists(t, filepath.Join(v.Path, "Inbox", "Asset 2.md"), "the other note is untouched")
}

func TestBodylessAssetNoteHasSingleBlankLineBeforeAttachments(t *testing.T) {
	// BDFL gate condition 7: a generic asset note has no body text, so
	// ## Attachments must sit exactly one blank line under the fence.
	r, v := testRunner(t)
	res, err := r.RunPush(context.Background(), assetRecord(1, "payload.bin", []byte("some bytes")))
	require.NoError(t, err)

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	require.Contains(t, string(note), "---\n\n## Attachments\n")
	require.NotContains(t, string(note), "---\n\n\n")
}

func TestRunPushOverThresholdGoesToCAS(t *testing.T) {
	r, v := testRunner(t)
	r.Vault.Assets.ThresholdBytes = 4 // force the CAS branch
	body := []byte("more than four bytes")

	res, err := r.RunPush(context.Background(), assetRecord(1, "big.bin", body))
	require.NoError(t, err)
	require.Equal(t, 1, res.New)

	require.NoDirExists(t, filepath.Join(v.Path, "Attachments"), "nothing lands in the vault")
	sum := sha256.Sum256(body)
	casPath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "pkms", "assets", hex.EncodeToString(sum[:])+".bin")
	require.FileExists(t, casPath)

	note, err := os.ReadFile(filepath.Join(v.Path, res.Notes[0]))
	require.NoError(t, err)
	require.Contains(t, string(note), casPath, "assets: ledger carries the machine-local path")
	require.Contains(t, string(note), "](file://", "external assets render as plain links, not wikilinks")
}
