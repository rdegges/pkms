package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The committed §31.12 eval fixtures (testdata/pdfeval/, provenance in
// manifest.json and scripts/gen-pdf-fixtures/). This file is untagged and
// asserts only the CURRENT §31.6 contract over them; the readability
// metric — which the incumbent is expected to fail on Identity-H
// producers — lives behind -tags pdfeval (pdfeval_test.go), because it
// judges candidates at the PR3 decision gate, not this library.

// pdfEvalDir holds the committed fixtures; §31.12 budgets keep the whole
// directory small enough to live in the repo (cap enforced below).
const pdfEvalDir = "testdata/pdfeval"

// pdfEvalManifestFile describes one document: committed fixtures and the
// maintainer's local real corpus (.context/pdf-corpus/manifest.json) use
// the same shape, so the pdfeval harness scores both with one code path.
type pdfEvalManifestFile struct {
	File     string   `json:"file"`
	Class    string   `json:"class"`
	Expect   string   `json:"expect"` // "text" | "no-text" | "encrypted"
	Producer string   `json:"producer"`
	Command  string   `json:"command"`
	Password string   `json:"password"`
	Phrases  []string `json:"phrases"`
}

type pdfEvalManifest struct {
	Fixtures []pdfEvalManifestFile `json:"fixtures"`
}

func loadPDFEvalManifest(t *testing.T, path string) []pdfEvalManifestFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var m pdfEvalManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	require.NotEmpty(t, m.Fixtures, "manifest %s lists no documents", path)
	return m.Fixtures
}

// The manifest is the classification of record (§31.12), so its own
// integrity is a test: all five corpus classes present, every file on
// disk, text fixtures carrying exactly 3 authored phrases of ≥4 words,
// and the encrypted fixture recording its (non-secret) user password.
func TestPDFEvalManifestIntegrity(t *testing.T) {
	fixtures := loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json"))

	classes := map[string]string{}
	for _, f := range fixtures {
		classes[f.Class] = f.Expect
		require.Contains(t, []string{"text", "no-text", "encrypted"}, f.Expect, "fixture %s", f.File)
		require.NotEmpty(t, f.Producer, "fixture %s records its producer", f.File)
		require.NotEmpty(t, f.Command, "fixture %s records its generation command", f.File)
		require.FileExists(t, filepath.Join(pdfEvalDir, f.File))
		if f.Expect == "text" {
			require.Len(t, f.Phrases, 3, "fixture %s: the metric needs exactly 3 phrases", f.File)
			for _, p := range f.Phrases {
				require.GreaterOrEqual(t, len(strings.Fields(p)), 4,
					"fixture %s: phrase %q must be ≥4 words (§31.12)", f.File, p)
			}
		}
		if f.Expect == "encrypted" {
			require.NotEmpty(t, f.Password, "the encrypted fixture must record its non-empty user password")
		}
	}
	for _, class := range []string{"word-export", "chrome-print", "latex-paper", "scan-image-only", "encrypted"} {
		require.Contains(t, classes, class, "§31.12 names five corpus classes")
	}
}

// The CURRENT contract over the committed fixtures — the assertions a
// §31.12 candidate swap must keep (and, for the honest-empty ones, flip
// to must-extract in the adoption amendment):
//   - text fixtures never yield control-byte garbage on any path,
//   - Identity-H producers (word-export, chrome-print) yield the honest
//     "no extractable text" outcome under the incumbent,
//   - the scan class yields the same honest empty outcome,
//   - the encrypted fixture maps to errPDFEncrypted.
func TestPDFEvalFixturesCurrentContract(t *testing.T) {
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		t.Run(f.Class, func(t *testing.T) {
			path := filepath.Join(pdfEvalDir, f.File)
			text, err := ExtractPDFText(path)
			switch f.Expect {
			case "encrypted":
				require.ErrorIs(t, err, errPDFEncrypted)
				require.Empty(t, text, "an encrypted document leaks no plaintext")
			case "no-text":
				// Incumbent quirk, pinned: ImageMagick writes a legal
				// "%PDF-1.4 " header (trailing space; qpdf --check passes)
				// that ledongthuc/pdf rejects outright, so today the scan
				// class degrades to the error hint instead of the honest
				// empty outcome. Either way NO text may be yielded. The
				// §31.12 candidate bar (honest empty) gates at PR3.
				if err != nil {
					require.ErrorContains(t, err, "invalid header",
						"the incumbent's only known failure here is its strict header check")
				}
				require.Empty(t, text, "a raster-only scan has no text layer to extract")
			case "text":
				require.NoError(t, err)
				for i, r := range text {
					if r == '\n' || r == '\t' || r == '\r' {
						continue
					}
					require.False(t, r < 0x20 || r == 0x7f,
						"control byte %#x at offset %d — notes are text files (§31.6)", r, i)
				}
				require.NotContains(t, pdfBody(path), "\x00", "no NUL byte may reach a note body")
				if f.Class == "word-export" || f.Class == "chrome-print" {
					// Identity-H subset fonts: the incumbent cannot decode
					// them, and "no extractable text" is the truthful answer
					// (§31.6). The §31.12 adoption amendment flips this
					// assertion to must-extract.
					require.Empty(t, text,
						"incumbent contract: Identity-H yields the honest empty outcome, never garbage")
				}
			default:
				t.Fatalf("unknown expect %q", f.Expect)
			}
		})
	}
}

// §31.12 keeps the committed corpus lightweight: everything under
// testdata/pdfeval/ (fixtures + manifest) must stay under 5 MiB total.
func TestPDFEvalFixturesSizeCap(t *testing.T) {
	var total int64
	err := filepath.WalkDir(pdfEvalDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	require.NoError(t, err)
	require.Positive(t, total, "the committed fixtures must exist")
	require.LessOrEqual(t, total, int64(5<<20),
		"committed eval fixtures exceed the 5 MiB cap (§31.12)")
}
