package ingest

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// Every pdfBody test in the suite used a SINGLE-page fixture, so nothing
// exercised neutralizePDFText's newline branch — a change that stripped `\n`
// along with the other control bytes would have shipped green while collapsing
// every real multi-page document into one run-on line. Real PDFs are
// multi-page; this is the ordinary case, not an edge case.
func TestPDFBodyKeepsEveryPageAndItsLineStructure(t *testing.T) {
	texts := make([]string, 12)
	for i := range texts {
		texts[i] = fmt.Sprintf("page %02d body", i)
	}
	p := writePDF(t, buildPagesPDF(texts))

	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	for _, want := range texts {
		require.Contains(t, text, want, "extraction dropped a page")
	}
	require.Equal(t, texts, splitPageTexts(text), "pages must arrive complete and in document order")

	body := pdfBody(p)
	// One \n per page boundary plus the trailing one (the §31.13 engine
	// emits single-line pages with no trailing newline of their own).
	require.GreaterOrEqual(t, strings.Count(body, "\n"), len(texts),
		"neutralization collapsed the page structure into one line: %q", body)
	require.Contains(t, body, "page 00 body")
	require.Contains(t, body, "page 11 body")
}

// splitPageTexts reduces extracted text to its non-empty lines, which is one
// per page for the single-Tj fixtures buildPagesPDF produces.
func splitPageTexts(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// The two output paths disagree about `\r`: neutralizePDFHint maps it to a
// space, neutralizePDFText deletes it. A document that encodes a line break as
// a bare CR therefore has words glued together in the note ("before"+"after"
// -> "beforeafter"). Asserted here only as "no control byte survives, and no
// text is lost", so either resolution (map to '\n', map to ' ') passes — see
// the report's commentary for the recommendation.
func TestPDFBodyNeutralizesCarriageReturnWithoutLosingText(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		contentStream(`before\015after`),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	p := writePDF(t, buildPDF(objs, ""))

	body := pdfBody(p)
	require.Contains(t, body, "before")
	require.Contains(t, body, "after")
	for _, r := range strings.TrimSuffix(body, "\n") {
		require.False(t, r < 0x20 || r == 0x7f,
			"note body carries control byte %#x: %q", r, body)
	}
}

// The hint cap slices at a byte offset and walks back to a rune boundary. No
// test ever put a multi-byte rune ON that boundary, so the backoff loop was
// never executed — a note is a UTF-8 file, and a cut mid-rune writes an
// invalid one. Seam-level since §31.13: the engine no longer echoes
// document bytes into errors, so the oversized payload is fed to the
// neutralizer directly (pdfBody wiring is pinned by the degrade tests).
func TestPDFBodyHintCutMidRuneStaysValidUTF8(t *testing.T) {
	// Two-byte runes: whatever the cap offset, some cut lands mid-rune.
	hint := neutralizePDFHint(strings.Repeat("é", 4096))
	require.True(t, utf8.ValidString(hint), "hint truncation cut a rune in half: %q", hint)
	require.LessOrEqual(t, len(hint), pdfHintCap+8, "hint exceeded its cap: %d bytes", len(hint))
	require.True(t, strings.HasSuffix(hint, "…"), "a truncated hint carries the ellipsis marker")
}

// The child is authenticated with a nonce passed BOTH in argv and in the
// environment, and the parent builds that environment as
// `append(os.Environ(), name+"="+nonce)`. That is only correct because
// os/exec de-duplicates in favour of the LAST value: a user (or a wrapper
// script, or a previous pkms child) whose environment already exports
// PKMS_PDF_EXTRACT_CHILD otherwise puts the stale value first, the child's
// os.Getenv returns it, authentication fails, and PDF extraction silently
// degrades to "extraction failed" for every document on that machine.
// Pinned because the whole nonce design rests on that stdlib detail.
func TestExtractPDFTextIgnoresInheritedChildEnvValue(t *testing.T) {
	t.Setenv(pdfChildEnv, "stale-inherited-value")

	p := writePDF(t, buildMinimalPDF("extraction survives a hostile environment"))
	text, err := ExtractPDFText(p)
	require.NoError(t, err, "an inherited %s must not break extraction", pdfChildEnv)
	require.Contains(t, text, "extraction survives a hostile environment")
}

// Re-ingesting the same PDF must produce the same body: the ingest pipeline
// dedups on content hash, so a body that varied between runs (map iteration
// over fonts, partial child output, a re-used temp file) would show up as
// unexplained note churn in a synced vault rather than as a test failure.
func TestExtractPDFTextIsDeterministic(t *testing.T) {
	p := writePDF(t, buildPagesPDF([]string{"alpha page", "beta page", "gamma page"}))

	first, err := ExtractPDFText(p)
	require.NoError(t, err)
	for i := 0; i < 4; i++ {
		got, err := ExtractPDFText(p)
		require.NoError(t, err)
		require.Equal(t, first, got, "extraction %d differed from the first run", i)
	}
}

// §31.6 caps EXTRACTED TEXT at 2 MiB, but the note body is written after
// escaping, and escaping every `[` doubles a bracket-heavy document. This
// bounds the real artefact — the bytes that land in the vault — so the
// amplification stays a known factor of two instead of growing silently if
// the neutralizer ever escapes more characters.
func TestPDFBodyIsBoundedOnTheSuccessPath(t *testing.T) {
	p := writePDF(t, buildPagesPDF([]string{strings.Repeat("[", 3<<20)}))

	body := pdfBody(p)
	require.LessOrEqual(t, len(body), 2*pdfTextCap+256,
		"a hostile PDF sized the note body at %d bytes; escaping must not amplify past a known bound", len(body))
	require.NotContains(t, body, "[[", "no bracket pair survives escaping")
	require.Equal(t, -1, firstLiveWikilinkOpener(body))
}

// The neutralizers are the last line of defense between attacker-controlled
// bytes and the vault, and they are pure functions of a string — so the
// invariant can be asserted over arbitrary input rather than over the handful
// of PDFs someone thought to build. Seeds cover the bypasses the PR #6 gate
// found (odd bracket runs, backslash doubling) plus the encoding edges.
// Runs the seed corpus in the normal suite; `go test -fuzz=FuzzNeutralize`
// hunts for new ones.
func FuzzNeutralizePDFText(f *testing.F) {
	for _, s := range neutralizerSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := neutralizePDFText(s)
		require.Equal(t, -1, firstLiveWikilinkOpener(out),
			"neutralized text minted a live wikilink from %q: %q", s, out)
		for _, r := range out {
			if r == '\n' || r == '\t' {
				continue
			}
			require.False(t, r < 0x20 || r == 0x7f,
				"control byte %#x survived neutralization of %q: %q", r, s, out)
		}
	})
}

func FuzzNeutralizePDFHint(f *testing.F) {
	for _, s := range neutralizerSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out := neutralizePDFHint(s)
		require.Equal(t, -1, firstLiveWikilinkOpener(out),
			"neutralized hint minted a live wikilink from %q: %q", s, out)
		require.NotContains(t, out, "\n", "a hint that spans lines escapes its blockquote: %q", out)
		for _, r := range out {
			require.False(t, r < 0x20 || r == 0x7f,
				"control byte %#x survived hint neutralization of %q: %q", r, s, out)
		}
		// The cap bounds the hint; the ellipsis and the escaping of a cut-off
		// bracket are the only growth past it.
		require.LessOrEqual(t, len(out), 2*pdfHintCap+8, "hint exceeded its bound: %d bytes", len(out))
		require.True(t, utf8.ValidString(out) || !utf8.ValidString(s),
			"neutralization made valid UTF-8 invalid: %q -> %q", s, out)
	})
}

func neutralizerSeeds() []string {
	return []string{
		"",
		"plain text",
		"[[Project Alpha]]",
		"[[[Project Alpha]]",
		"![[secret.png]]",
		`\[[Project Alpha]]`,
		`\\[[Project Alpha]]`,
		"[[[[[[deep]]]]]]",
		"citation [12] survives",
		"\x00\x01\x02 control \x7f bytes",
		"\x1b]0;pwned\x07",
		"line one\nline two\ttabbed\r\ncrlf",
		"café naïve 中文 \U0001f600",
		"\xff\xfe invalid utf8",
		strings.Repeat("é", 400),
		strings.Repeat("[", 600),
	}
}
