package ingest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// firstLiveWikilinkOpener returns the offset of the first `[[` that a
// markdown renderer will treat as a wikilink opener, or -1. A bracket is
// escaped only by an ODD run of preceding backslashes: `\[[` is inert, but
// `\\[[` (an even run — the two backslashes escape each other) is live.
// Parity-aware so it cannot pass a fix that only handles the odd case.
func firstLiveWikilinkOpener(s string) int {
	for i := 0; i+1 < len(s); i++ {
		if s[i] != '[' || s[i+1] != '[' {
			continue
		}
		bs := 0
		for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
			bs++
		}
		if bs%2 == 0 { // even run → the first `[` is not escaped → live pair
			return i
		}
	}
	return -1
}

// --- gate proofs (were RED at the PR #6 review; now pinned green) -----

// REGRESSION (fixed; pinned): the wikilink neutralizer escapes only the
// FIRST bracket of each `[[` pair, so an odd-length bracket run walks right
// through it. `[[[secret]]` becomes `\[[[secret]]`: the backslash consumes
// bracket 1, and brackets 2-3 open a live graph edge. pdf.go's own contract
// says a PDF may mint neither embeds (`![[`) nor graph edges (`[[`).
//
// Both output paths are affected — extracted text and the failure hint —
// because both end in the same one-pair ReplaceAll.
//
// Fix: escape every `[` (`strings.ReplaceAll(s, "[", "\\[")`) or drop the
// opener outright. Escaped single brackets still render as `[`, so `[12]`
// citations survive either way.
func TestPDFBodyNeverMintsLiveWikilink(t *testing.T) {
	t.Run("extracted_text_path", func(t *testing.T) {
		p := writePDF(t, buildMinimalPDF("see [[[secret]] here"))
		body := pdfBody(p)
		require.Equal(t, -1, firstLiveWikilinkOpener(body),
			"extracted PDF text minted a live wikilink at offset %d of %q",
			firstLiveWikilinkOpener(body), body)
	})

	t.Run("failure_hint_path", func(t *testing.T) {
		// 14-byte swap: a string dict key the parser rejects and echoes back.
		p := writePDF(t, sameLenMutate(t, buildMinimalPDF("seed"), "/Type /Catalog", "([[[x.png]]) 1"))
		body := pdfBody(p)
		require.Equal(t, -1, firstLiveWikilinkOpener(body),
			"the extraction-failure hint minted a live wikilink at offset %d of %q",
			firstLiveWikilinkOpener(body), body)
	})
}

// REGRESSION (fixed; pinned): §31.6 caps extracted text at 2 MiB, but the
// FAILURE path has no cap at all. github.com/ledongthuc/pdf echoes the
// offending bytes into its panic message, so a PDF whose dictionary key is a
// 3 MiB string produces a 3 MiB note body — on a single line, because
// neutralizePDFHint collapses newlines to spaces. Observed: 3,145,824 bytes,
// one line.
//
// The whole point of the cap is that a hostile document cannot decide how
// big a note is. Fix: truncate the hint (a few hundred bytes is plenty for a
// diagnostic) before it reaches the body.
func TestPDFBodyHintIsBounded(t *testing.T) {
	big := strings.Repeat("A", 3<<20)
	objs := []string{
		"<< (" + big + ") 1 /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		contentStream("hello"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	p := writePDF(t, buildPDF(objs, ""))

	body := pdfBody(p)
	require.LessOrEqual(t, len(body), pdfTextCap,
		"a hostile PDF sized the note body at %d bytes; §31.6's %d-byte cap must bound EVERY body path, not just the success path",
		len(body), pdfTextCap)
}

// REGRESSION (fixed; pinned): the undecodable-output guard is
// document-wide, so one page in a subset CID font discards the text of every
// other page. Here page 1 is plain Helvetica carrying real text and page 2 is
// Identity-H; the note ends up asserting "The PDF contains no extractable
// text", which is false.
//
// That is the §31.6 honesty ruling inverted: the ruling exists so notes do
// not carry garbage, not so notes carry a confident lie. It also contradicts
// extractPDFInProcess's own "one broken page never sinks the document".
// Mixed-producer PDFs (a Word body with a scanned or re-embedded page) are
// ordinary, not hostile.
//
// Fix: apply the undecodable test per page and keep the pages that decode.
func TestPDFBodyDoesNotFalselyClaimNoText(t *testing.T) {
	const good = "IMPORTANT CONTRACT TEXT"
	cid := "BT /F2 12 Tf 72 700 Td <003A00440059> Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 8 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		contentStream(good),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /AAAAAA+Test /Encoding /Identity-H " +
			"/DescendantFonts [7 0 R] >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /AAAAAA+Test /CIDSystemInfo " +
			"<< /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /DW 1000 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 9 0 R " +
			"/Resources << /Font << /F2 6 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cid), cid),
	}
	p := writePDF(t, buildPDF(objs, ""))

	body := pdfBody(p)
	require.NotContains(t, body, "no extractable text",
		"the note claims the PDF has no extractable text, but page 1 decodes to %q; got body %q", good, body)
}

// --- containment properties that hold today (regression pins) ---------

// The failure hint is written straight out of a hostile file, so a PDF that
// gets a terminal escape sequence into the library's error text would repaint
// the user's terminal (and the note) on `cat`. Pinned: control bytes are
// stripped from the hint.
func TestPDFBodyHintStripsTerminalEscapes(t *testing.T) {
	// 14 bytes: an OSC title-set sequence wrapped in a string dict key.
	p := writePDF(t, sameLenMutate(t, buildMinimalPDF("seed"), "/Type /Catalog", "(\x1b]0;pwned\x07) 1"))

	body := pdfBody(p)
	require.Contains(t, body, "> Text extraction failed:", "the note still gets a hint")
	require.Contains(t, body, "]0;pwned", "the fixture must actually reach the hint text")
	for _, r := range strings.TrimSuffix(body, "\n") {
		require.False(t, r < 0x20 || r == 0x7f,
			"hint carries control byte %#x; got %q", r, body)
	}
}

// A parser error carrying the document's own newlines must still collapse to
// one blockquote line — a hint that spans lines would break out of the `> `
// quote and turn attacker bytes into note-level markdown.
func TestPDFBodyHintStaysOneLineWhenErrorIsMultiline(t *testing.T) {
	// 14 bytes, two embedded newlines inside a string dict key.
	p := writePDF(t, sameLenMutate(t, buildMinimalPDF("seed"), "/Type /Catalog", "(ab\ncd\nefgh) 1"))

	body := pdfBody(p)
	require.True(t, strings.HasPrefix(body, "> Text extraction failed: "), "got %q", body)
	require.Equal(t, 1, strings.Count(body, "\n"), "multi-line error leaked extra lines: %q", body)
	require.Contains(t, body, "ab cd efgh", "newlines collapse to spaces, text is kept")
}

// Extraction infrastructure failure is not the document's fault and must not
// fail the ingest: with no usable temp directory the record still lands, with
// a hint instead of text.
func TestExtractPDFTextDegradesWhenTempUnavailable(t *testing.T) {
	p := writePDF(t, buildMinimalPDF("hello"))
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))

	text, err := ExtractPDFText(p)
	require.Error(t, err, "a missing temp dir is an error, not silent success")
	require.Empty(t, text)

	rec, ferr := FileRecord(p, testTypes, testNow)
	require.NoError(t, ferr, "infrastructure failure must never fail the record")
	require.Equal(t, "asset", rec.NoteType)
	require.Contains(t, rec.Body, "> Text extraction failed:")
	require.Len(t, rec.Assets, 1)
}

// The child hook is reachable by anything that can set one environment
// variable, so its argument handling is a real boundary: a wrong argument
// count must exit with the error code and write nothing.
// A process carrying the child sentinel is authenticated by the per-run
// nonce: a wrong/absent nonce or a bad arg count exits pdfExitError and
// writes nothing (SPEC §31.6 child-auth). The sentinel is what marks a
// child at all — without it, the env var alone never selects the child
// (that invariant is the clobber-safety test, e2e 22).
func TestPDFExtractChildMainRejectsBadAuth(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	scratch := filepath.Join(t.TempDir(), "out")

	cases := map[string]struct {
		args []string
		env  string
	}{
		"nonce mismatch":  {[]string{pdfChildSentinel, "GOODNONCE", "in.pdf", scratch}, "OTHERNONCE"},
		"empty env nonce": {[]string{pdfChildSentinel, "GOODNONCE", "in.pdf", scratch}, ""},
		"too few args":    {[]string{pdfChildSentinel, "GOODNONCE"}, "GOODNONCE"},
		"too many args":   {[]string{pdfChildSentinel, "GOODNONCE", "in.pdf", scratch, "extra"}, "GOODNONCE"},
	}
	for name, c := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			cmd := exec.Command(exe, c.args...)
			cmd.Env = append(os.Environ(), pdfChildEnv+"="+c.env)
			out, runErr := cmd.CombinedOutput()

			var ee *exec.ExitError
			require.ErrorAs(t, runErr, &ee, "child must exit non-zero, got output %q", out)
			require.Equal(t, pdfExitError, ee.ExitCode())
			require.Empty(t, string(out), "the child never writes to stdout/stderr")
			require.NoFileExists(t, scratch, "a failed-auth child writes no output file")
		})
	}
}

// Extraction spawns a child and marshals through a temp file; a bulk ingest
// runs many of these. Concurrent calls must neither cross-contaminate their
// temp files nor race on the shared package state.
func TestExtractPDFTextIsSafeInParallel(t *testing.T) {
	const n = 16
	paths := make([]string, n)
	for i := range paths {
		paths[i] = writePDF(t, buildMinimalPDF("document number "+strconv.Itoa(i)))
	}

	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := range paths {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = ExtractPDFText(paths[i])
		}(i)
	}
	wg.Wait()

	for i := range paths {
		require.NoError(t, errs[i], "concurrent extraction %d failed", i)
		require.Contains(t, got[i], "document number "+strconv.Itoa(i),
			"concurrent extraction %d got another document's text: %q", i, got[i])
	}
}

// Every extraction creates a temp file for the child to write into; a vault
// ingesting thousands of PDFs must not leave them behind.
func TestExtractPDFTextLeavesNoTempFiles(t *testing.T) {
	pattern := filepath.Join(os.TempDir(), "pkms-pdftext-*")
	before, err := filepath.Glob(pattern)
	require.NoError(t, err)

	good := writePDF(t, buildMinimalPDF("kept clean"))
	bad := writePDF(t, []byte("%PDF-1.5\nnot really a pdf\n"))
	for i := 0; i < 5; i++ {
		_, _ = ExtractPDFText(good)
		_, _ = ExtractPDFText(bad)
	}

	after, err := filepath.Glob(pattern)
	require.NoError(t, err)
	require.Len(t, after, len(before), "extraction leaked temp files: %v", after)
}
