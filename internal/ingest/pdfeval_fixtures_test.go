package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"
)

// The committed §31.12 eval fixtures (testdata/pdfeval/, provenance in
// manifest.json and scripts/gen-pdf-fixtures/). This file is untagged and
// carries the §31.13 contract over them, readability assertions included —
// the adoption amendment promoted those into blocking CI. The full
// scorecard over the maintainer's larger local corpus stays behind
// -tags pdfeval (pdfeval_test.go); it is a measurement tool, not a gate.

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

// normalizePDFEvalText applies the frozen §31.12 normalization, in its
// order: Unicode NFKC (folds the ligatures LaTeX loves, e.g. ﬃ → ffi),
// lowercase, join "-\n" line-break hyphenation, collapse whitespace runs
// to single spaces. Phrases get the same treatment before containment.
// It lives here, untagged, because it IS the frozen metric: the tagged
// scorecard is never compiled or run by CI, so the metric's own unit
// tests (pdfeval_metric_test.go) have to run under `go test ./...`.
func normalizePDFEvalText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-\n", "")
	return strings.Join(strings.Fields(s), " ")
}

// repoRoot walks up from the package directory to the go.mod, so tests
// find repo-root paths (.context/pdf-corpus/, scripts/gen-pdf-fixtures/)
// regardless of which directory the test binary runs from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above %s", dir)
		dir = parent
	}
}

func loadPDFEvalManifest(t *testing.T, path string) []pdfEvalManifestFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var m pdfEvalManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	require.NotEmpty(t, m.Fixtures, "manifest %s lists no documents", path)
	// §31.12 integrity rules apply to EVERY manifest — the committed
	// fixtures and the maintainer's real corpus alike. A text entry
	// without its 3 authored phrases is evidence-free: it must fail the
	// run, never score PASS toward the ceil(0.8 x N) adoption bar
	// (adoption fails closed).
	for _, f := range m.Fixtures {
		require.Contains(t, []string{"text", "no-text", "encrypted"}, f.Expect,
			"%s: entry %s has unknown expect %q", path, f.File, f.Expect)
		if f.Expect != "text" {
			continue
		}
		require.Len(t, f.Phrases, 3,
			"%s: text entry %s needs exactly 3 ground-truth phrases (§31.12)", path, f.File)
		for _, phr := range f.Phrases {
			require.GreaterOrEqual(t, len(strings.Fields(phr)), 4,
				"%s: entry %s phrase %q must be ≥4 words (§31.12)", path, f.File, phr)
		}
	}
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

// The §31.13 contract over the committed fixtures — the readability
// assertions PROMOTED to untagged, blocking CI by the adoption amendment
// (each run pays the ~1.1s per-child wasm compile; five fixtures ≈ 6s,
// accepted at the plan gate):
//   - text fixtures MUST extract, and must contain ALL 3 of their
//     manifest ground-truth phrases under the frozen §31.12 metric
//     (under the previous engine all three were honest-empty — the
//     baseline PR #20 recorded),
//   - no text fixture may ever yield control-byte garbage on any path,
//   - the scan class yields the honest empty outcome (the previous
//     engine rejected its spec-legal "%PDF-1.4 " trailing-space header
//     outright; the §31.13 engine parses it and finds no text layer),
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
				require.NoError(t, err, "a well-formed raster-only scan parses cleanly")
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
				got := normalizePDFEvalText(text)
				for _, phrase := range f.Phrases {
					require.Contains(t, got, normalizePDFEvalText(phrase),
						"%s must contain its ground-truth phrase (§31.13 must-extract)", f.File)
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
