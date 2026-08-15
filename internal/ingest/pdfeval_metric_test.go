package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The §31.12 metric and manifest, tested as the load-bearing artifacts
// they are. normalizePDFEvalText decides — alone — whether a candidate
// extractor is ADOPTABLE, and the manifest's phrases are the ground
// truth it decides against. Neither had a test: the scorecard that uses
// them is behind -tags pdfeval, which CI never compiles as a test binary
// and never runs, so a silent change to the metric would surface only at
// the PR3 decision gate, on the numbers it corrupted.

// The frozen normalization, case by case: NFKC, lowercase, "-\n" join,
// whitespace collapse — in that order. The last two cases pin LIMITS of
// the frozen rule (CRLF and soft-hyphen line breaks do NOT join), which
// is what a candidate extractor's own line-ending behavior will hit.
func TestPDFEvalNormalizationIsFrozen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text lowercases", "The Efficient Meter", "the efficient meter"},
		{"nfkc folds the ffi ligature", "eﬃcient parser", "efficient parser"},
		{"nfkc folds the fi ligature", "the ﬁle on disk", "the file on disk"},
		{"nfkc folds fullwidth forms", "ＰＫＭＳ vault", "pkms vault"},
		{"nfkc turns nbsp into a collapsible space", "a\u00a0b", "a b"},
		{"lowercase covers non-ascii", "ÉTÉ Über", "été über"},
		{"hyphenation join", "hyphen-\nated word", "hyphenated word"},
		{"hyphenation join runs before whitespace collapse", "single-\npass  parser", "singlepass parser"},
		{"a real hyphen inside a line survives", "well-known result", "well-known result"},
		{"whitespace runs collapse", " a\t\tb\n\n c \r\n", "a b c"},
		{"empty stays empty", "", ""},
		{"whitespace only collapses to empty", " \t\n\r ", ""},
		// Pinned limits of the frozen rule — not defects, but the reason a
		// candidate that emits CRLF line endings can score FAIL on text it
		// actually extracted. §31.12 froze "-\n"; anything else is a spec
		// amendment, not a test change.
		{"crlf hyphenation does NOT join (frozen rule is -\\n)", "hyphen-\r\nated word", "hyphen- ated word"},
		{"soft hyphen line break does NOT join", "soft\u00ad\nwrap", "soft\u00ad wrap"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, normalizePDFEvalText(tc.in))
		})
	}
}

// Phrases and extracted text go through the same function, so the metric
// is only meaningful if normalization is a fixed point: normalizing an
// already-normalized phrase must not change it, or a phrase could fail
// to match text that literally contains it.
func TestPDFEvalNormalizationIsIdempotent(t *testing.T) {
	inputs := []string{
		"eﬃcient parser reads the whole file",
		"hyphen-\nated word", " a\t\tb\n\n c ", "ＰＫＭＳ", "ÉTÉ", "",
		"binary—each document either passes or fails",
	}
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		inputs = append(inputs, f.Phrases...)
	}
	for _, in := range inputs {
		once := normalizePDFEvalText(in)
		require.Equal(t, once, normalizePDFEvalText(once), "not a fixed point for %q", in)
	}
}

// A phrase that does not exist in the AUTHORED SOURCE is not ground
// truth — it is whatever the extractor happened to emit. This is the
// anti-co-tuning check §31.12 leans on: the manifest is only trustworthy
// if every phrase traces back to the document the maintainer wrote, not
// to a candidate's output. It also catches a fixture regenerated from an
// edited source without the phrases being re-authored.
func TestPDFEvalPhrasesComeFromTheAuthoredSource(t *testing.T) {
	src := filepath.Join(repoRoot(t), "scripts", "gen-pdf-fixtures", "src")
	// The encrypted fixture is the word-export document with a password
	// applied (gen-encrypted.sh), so it shares that source.
	sourceOf := map[string]string{
		"word-export":  "word-export.fodt",
		"chrome-print": "chrome-print.html",
		"latex-paper":  "latex-paper.tex",
		"encrypted":    "word-export.fodt",
	}
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		if len(f.Phrases) == 0 {
			continue // the scan class is text-free by construction
		}
		name, ok := sourceOf[f.Class]
		require.True(t, ok, "class %s carries phrases but no authored source is mapped", f.Class)
		raw, err := os.ReadFile(filepath.Join(src, name))
		require.NoError(t, err)
		text := normalizePDFEvalText(string(raw))
		for _, p := range f.Phrases {
			require.Contains(t, text, normalizePDFEvalText(p),
				"phrase %q is not in the authored source %s — ground truth must come from the document, not the extractor", p, name)
		}
	}
}

// Provenance is the fixtures' only defense against "where did this
// binary come from": the manifest's command field names the script that
// produced the file. A command naming a script that no longer exists is
// unfalsifiable provenance, and a script no manifest entry references is
// a fixture generator with no fixture.
func TestPDFEvalProvenanceScriptsExist(t *testing.T) {
	root := repoRoot(t)
	referenced := map[string]bool{}
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		rel, _, found := strings.Cut(f.Command, ":")
		require.True(t, found, "fixture %s: command must start with the generating script path", f.File)
		require.True(t, strings.HasPrefix(rel, "scripts/gen-pdf-fixtures/"),
			"fixture %s: provenance script %q must live in scripts/gen-pdf-fixtures/", f.File, rel)
		info, err := os.Stat(filepath.Join(root, rel))
		require.NoError(t, err, "fixture %s names a provenance script that does not exist", f.File)
		require.NotZero(t, info.Mode().Perm()&0o111, "%s must be executable to be re-runnable", rel)
		referenced[filepath.Base(rel)] = true
	}

	entries, err := os.ReadDir(filepath.Join(root, "scripts", "gen-pdf-fixtures"))
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		require.True(t, referenced[e.Name()],
			"generator %s produces no fixture the manifest records", e.Name())
	}
}

// The manifest is keyed by neither field in code — TestPDFEvalManifest
// Integrity builds a class map that a duplicate would silently overwrite,
// hiding a missing class behind a copied entry.
func TestPDFEvalManifestEntriesAreUnique(t *testing.T) {
	files, classes := map[string]bool{}, map[string]bool{}
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		require.False(t, files[f.File], "duplicate fixture file %s", f.File)
		require.False(t, classes[f.Class], "duplicate corpus class %s", f.Class)
		files[f.File], classes[f.Class] = true, true
	}
}
