package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/assets"
	"github.com/rdegges/pkms/internal/fetch"
)

// The asset filename is derived from the FINAL URL path, which is
// server-controlled. Whatever shape it takes, the stored attachment must
// land inside the attachments dir as a real file — these are the URL shapes
// that make path.Base return something that is not a filename.
func TestURLAssetFilenameFromOddURLShapes(t *testing.T) {
	body := []byte("%PDF-1.5 payload bytes")
	urls := []string{
		"https://example.com/doc.pdf",
		"https://example.com/doc.pdf?download=1&v=2",
		"https://example.com/path/to/",   // path.Base → "to"
		"https://example.com/",           // path.Base → "/"
		"https://example.com",            // empty path, path.Base → "."
		"https://example.com/..",         // traversal in the path
		"https://example.com/%2e%2e%2fx", // encoded traversal
	}
	for _, raw := range urls {
		t.Run(raw, func(t *testing.T) {
			g := fakeDownloader{t: t, body: body}
			rec, cleanup, err := URLRecord(context.Background(), g, raw, testTypes, noHooks, 0, testNow)
			defer cleanup()
			require.NoError(t, err, "push mode never refuses a sniffed type (SPEC §31.1)")
			require.Equal(t, "asset", rec.NoteType)
			require.Len(t, rec.Assets, 1)

			vaultRoot := t.TempDir()
			p := assets.Policy{
				VaultRoot: vaultRoot, AttachmentsDir: "Attachments",
				Threshold: 1 << 20, CASDir: filepath.Join(t.TempDir(), "cas"),
			}
			st, err := p.Store(assets.Source{
				Filename: rec.Assets[0].Filename, SHA256: rec.Assets[0].SHA256,
				Size: rec.Assets[0].Size, Open: rec.Assets[0].Open,
			})
			require.NoError(t, err)
			require.True(t, st.InVault)
			require.True(t, strings.HasPrefix(st.Path, "Attachments/"),
				"a server-controlled name must not escape the attachments dir: %q", st.Path)
			base := strings.TrimPrefix(st.Path, "Attachments/")
			require.NotContains(t, base, "/")
			require.NotEqual(t, "..", base)

			onDisk := filepath.Join(vaultRoot, filepath.FromSlash(st.Path))
			got, err := os.ReadFile(onDisk)
			require.NoError(t, err)
			require.Equal(t, body, got)
			t.Logf("URL path %q → asset name %q → stored %q",
				raw, rec.Assets[0].Filename, base)
		})
	}
}

// The remote asset's title is the URL as typed and `source` is never
// rewritten (SPEC §20/§21), even when the fetch redirected.
func TestURLAssetKeepsSourceVerbatimAcrossRedirect(t *testing.T) {
	g := fakeDownloader{
		t:        t,
		body:     []byte("%PDF-1.5 payload"),
		finalURL: "https://cdn.example.net/blob/9f8e.pdf",
	}
	rec, cleanup, err := URLRecord(context.Background(), g, "https://example.com/dl?id=7", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "https://example.com/dl?id=7", rec.Fields["source"], "source is verbatim")
	require.Equal(t, "https://cdn.example.net/blob/9f8e.pdf", rec.Fields["fetched_url"])
	require.Equal(t, "https://example.com/dl?id=7", rec.Fields["title"])
	require.Equal(t, "https://example.com/dl?id=7", rec.NaturalKey, "dedup keys on the requested URL")
	require.Equal(t, "9f8e.pdf", rec.Assets[0].Filename, "the filename comes from the final URL")
}

// mime is the sniffed type with any parameters stripped (SPEC §31.4).
func TestAssetMimeStripsCharsetParameter(t *testing.T) {
	// A zip sniffs as application/zip; a UTF-16 doc sniffs with a charset
	// parameter that must not leak into the `mime` field.
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.txt")
	require.NoError(t, os.WriteFile(p, []byte{0xff, 0xfe, 'h', 0, 'i', 0}, 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err)
	if rec.NoteType == "asset" {
		mime, _ := rec.Fields["mime"].(string)
		require.NotContains(t, mime, ";", "mime carries no parameters")
		require.NotContains(t, mime, "charset")
		require.Equal(t, strings.TrimSpace(mime), mime, "no stray whitespace")
	}
}

// A local asset's NaturalKey is the content hash, so two copies at different
// paths dedup — and the hash matches the stamped sha256.
func TestFileAssetKeyIsContentHash(t *testing.T) {
	dir := t.TempDir()
	body := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 9, 9, 9}
	a := filepath.Join(dir, "one.png")
	b := filepath.Join(dir, "two.png")
	require.NoError(t, os.WriteFile(a, body, 0o644))
	require.NoError(t, os.WriteFile(b, body, 0o644))

	ra, err := FileRecord(context.Background(), a, testTypes, noHooks, testNow)
	require.NoError(t, err)
	rb, err := FileRecord(context.Background(), b, testTypes, noHooks, testNow)
	require.NoError(t, err)

	sum := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(sum[:]), ra.NaturalKey)
	require.Equal(t, ra.NaturalKey, rb.NaturalKey, "same bytes, different path → same key")
	require.Equal(t, ra.NaturalKey, ra.Fields["sha256"])
	require.Equal(t, ra.NaturalKey, ra.Assets[0].SHA256, "the record and its asset agree on the hash")
	require.Equal(t, int64(len(body)), ra.Assets[0].Size)
}

// SPEC §31.3 rescopes the 10 MiB read cap to HTML/text kinds only: an
// asset-kind local file is streamed under no size cap, because the threshold
// decides placement, not admissibility. This is the behavioral difference
// that lets a user push a 2 GB video.
func TestFileAssetHasNoSizeCap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	// Larger than fetch.MaxHTMLBody (10 MiB), sniffing as a binary.
	buf := make([]byte, fetch.MaxHTMLBody+4096)
	copy(buf, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})
	require.NoError(t, os.WriteFile(p, buf, 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err, "an over-10-MiB binary is admissible (SPEC §31.3)")
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, int64(len(buf)), rec.Assets[0].Size)
	require.NotEmpty(t, rec.Assets[0].LocalPath)
}

// The page kinds keep their cap, and the error copy is unchanged (SPEC §20).
func TestFileTextKeepsTheTenMiBCap(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.txt")
	require.NoError(t, os.WriteFile(p, []byte(strings.Repeat("a", fetch.MaxHTMLBody+1)), 0o644))

	_, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.ErrorContains(t, err, "exceeds the 10 MiB ingest limit")
}

// PROPOSED CONTRACT (SPEC §31.5, advisory dedup pre-check) — not implemented
// on this branch, so this is a marker rather than a failing gate. The spec
// requires push mode to check the NaturalKey against the state store and the
// vault source-id set BEFORE downloading, so a re-pushed 50 MiB video does
// not re-download just to dedup at emit time. Today URLRecord downloads
// unconditionally and the only dedup is the emit-time check in run().
// Implementing it is a design decision (where the check lives, and how the
// CLI reports an early no-op), so it is reported, not asserted.
func TestProposedAdvisoryDedupPreCheckBeforeDownload(t *testing.T) {
	t.Skip("SPEC §31.5 advisory dedup pre-check DEFERRED by BDFL ruling at the PR #5 gate " +
		"to the hooks PR (phase-2.5 PR4), where re-transcription makes it load-bearing; " +
		"until then the worst case is a bounded re-download on a user-initiated push and " +
		"emit-time dedup keeps correctness. This skip is the tracking artifact.")
}
