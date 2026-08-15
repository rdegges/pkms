package ingest

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// The eval fixtures are the only REAL-PRODUCER PDFs in the repo — every
// other §31.6 test runs on the in-repo golden builder or hand-mutated
// bytes. So they are also the only place the note-body contract is
// exercised end to end against LibreOffice, Chromium, tectonic,
// ImageMagick and qpdf output. §31.12 lists these invariants as the ones
// any extractor swap preserves, which makes them worth asserting on the
// corpus a swap will be judged on, not only on synthetic input.

// Whatever the extractor does with a real-producer PDF, what lands in the
// vault is a text file: valid UTF-8, no control bytes, no minted
// wikilinks or embeds, and never empty (an empty body would file a note
// with no explanation of why).
func TestPDFEvalFixtureNoteBodiesAreSafe(t *testing.T) {
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		t.Run(f.Class, func(t *testing.T) {
			t.Parallel()
			body := pdfBody(filepath.Join(pdfEvalDir, f.File))

			require.True(t, utf8.ValidString(body), "note bodies are UTF-8 text files (§31.6)")
			require.NotEmpty(t, body, "every outcome writes a body, even the empty and error ones")
			require.True(t, strings.HasSuffix(body, "\n"), "body %q must end in a newline", body)
			for i, r := range body {
				if r == '\n' || r == '\t' {
					continue
				}
				require.False(t, r < 0x20 || r == 0x7f,
					"control byte %#x at offset %d in the note body (§31.6)", r, i)
			}
			require.NotContains(t, body, "[[",
				"a PDF may not mint a graph edge or embed in the vault (§28.9)")
		})
	}
}

// The encrypted class exists to prove the failure hint is a hint. A
// bounded one-line diagnostic must not become a side channel for the
// plaintext the password protects.
func TestPDFEvalEncryptedFixtureLeaksNoPlaintext(t *testing.T) {
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		if f.Expect != "encrypted" {
			continue
		}
		body := pdfBody(filepath.Join(pdfEvalDir, f.File))
		require.Contains(t, body, "encrypted", "the note lands with a one-line hint")
		require.NotContains(t, body, f.Password, "the hint never echoes the password")
		for _, p := range f.Phrases {
			require.NotContains(t, normalizePDFEvalText(body), normalizePDFEvalText(p),
				"the encrypted document's text leaked into the note body")
		}
	}
}

// The §31.12 scorecard is only comparable run to run if extraction is
// deterministic: a candidate that walks a font map in Go map order, or
// caches across pages, can score PASS on one run and FAIL on the next,
// and the adoption decision would be noise. Extraction also crosses a
// process boundary and a temp file, so this doubles as the repeat-use
// smoke test on real-producer input.
//
// Every extraction re-execs the test binary, which is expensive under
// -race (CI's mode): the repeat count stays at the minimum that can
// observe a difference, and the classes run in parallel — proven safe by
// TestExtractPDFTextIsSafeInParallel.
func TestPDFEvalFixtureExtractionIsDeterministic(t *testing.T) {
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		t.Run(f.Class, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(pdfEvalDir, f.File)
			wantText, wantErr := ExtractPDFText(path)
			text, err := ExtractPDFText(path)
			require.Equal(t, wantText, text, "a repeat extraction returned different text")
			if wantErr == nil {
				require.NoError(t, err, "a repeat extraction failed where the first succeeded")
				return
			}
			require.EqualError(t, err, wantErr.Error(), "a repeat extraction returned a different error")
		})
	}
}
