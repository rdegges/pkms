package ingest

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// sanitizePDFPageText is the whole §31.13 per-page honesty screen — it
// decides, for every page of every ingested PDF, between "real prose with a
// few unmappable glyphs" and "undecoded glyph-id output that must never
// reach a note". It arrived with no direct test: the only coverage was
// whole-document extractions, which exercise one point on its threshold and
// cannot reach the boundary at all. These are pure-function tests, so they
// cost no wasm compile.

// The 10% rule, at and around its boundary. §31.13 freezes the ratio: a
// page is dropped only when MORE than 10% of its runes were control bytes.
func TestSanitizePDFPageTextGarbageThreshold(t *testing.T) {
	const marker = '\x02' // pdfium's "no text mapping for this glyph"

	cases := map[string]struct {
		in          string
		wantGarbage bool
	}{
		// The honesty half: real prose keeps its page even with markers.
		"clean prose": {"the quick brown fox", false},
		"prose with one marker in ten runes (exactly 10%)": {
			strings.Repeat("a", 9) + string(marker), false,
		},
		"prose with ten markers in a hundred runes (exactly 10%)": {
			strings.Repeat("a", 90) + strings.Repeat(string(marker), 10), false,
		},
		// The garbage half: past the ratio, the page dies whole.
		"eleven markers in a hundred and one runes (just over 10%)": {
			strings.Repeat("a", 90) + strings.Repeat(string(marker), 11), true,
		},
		"two markers in ten runes": {
			strings.Repeat("a", 8) + strings.Repeat(string(marker), 2), true,
		},
		"all markers — the undecoded glyph-id signature": {
			strings.Repeat(string(marker), 64), true,
		},
		"NUL-laced glyph ids (the pre-§31.13 failure mode)": {
			"\x00:\x00D\x00Y\x00H", true,
		},
		// Whitespace is text, not garbage, however much of it there is.
		"whitespace only":                {"\n\t\r\n", false},
		"prose with real line structure": {"line one\nline two\tcol\r\n", false},
		// DEL is a control byte too (§31.6 treats 0x7f with the C0 set).
		"del bytes past the ratio": {strings.Repeat("a", 4) + strings.Repeat("\x7f", 2), true},
		// Degenerate input must not divide by zero or claim garbage.
		"empty page": {"", false},
	}

	for name, c := range cases {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			out, garbage := sanitizePDFPageText(c.in)
			require.Equal(t, c.wantGarbage, garbage,
				"§31.13 ratio misjudged %q", c.in)
			requireNoControlBytesExceptWhitespace(t, out)
		})
	}
}

// A kept page carries its prose and its line structure, with every
// unmappable glyph VISIBLE as U+FFFD rather than silently dropped — §31.13
// chose visible over silent so a note never implies text it did not have.
func TestSanitizePDFPageTextMarksUnmappableGlyphsVisibly(t *testing.T) {
	out, garbage := sanitizePDFPageText("Total: 42\x02 USD\nnext line\tcol")
	require.False(t, garbage)
	require.Equal(t, "Total: 42� USD\nnext line\tcol", out,
		"markers become U+FFFD; \\n and \\t pass through untouched")
}

// The child hands its output to the parent as bytes; §31.6 requires a note
// body to be a UTF-8 text file, so the sanitizer may never turn invalid
// input into invalid output.
func TestSanitizePDFPageTextAlwaysReturnsValidUTF8(t *testing.T) {
	for name, in := range map[string]string{
		"lone continuation byte": "\x80\x81 text",
		"truncated 3-byte rune":  "text \xe4\xb8",
		"raw high bytes":         "\xff\xfe\xff\xfe",
		"mixed valid and not":    "café \xff naïve \U0001f600",
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			out, _ := sanitizePDFPageText(in)
			require.True(t, utf8.ValidString(out),
				"sanitized page text must be valid UTF-8, got %q", out)
			requireNoControlBytesExceptWhitespace(t, out)
		})
	}
}

// The screen is the last thing between engine output and a note body, and
// it is a pure function of a string — so its two invariants can be asserted
// over arbitrary input instead of the handful of pages someone thought to
// build. Seeds cover the pdfium marker, the ratio boundary, and the
// encoding edges. Runs the seed corpus in the normal suite;
// `go test -fuzz=FuzzSanitizePDFPageText` hunts for more.
func FuzzSanitizePDFPageText(f *testing.F) {
	for _, s := range []string{
		"",
		"plain page text",
		"\x02",
		strings.Repeat("a", 9) + "\x02",
		strings.Repeat("\x02", 32),
		"\x00\x01\x02\x1f\x7f",
		"line\nbreak\ttab\rcr",
		"café naïve 中文 \U0001f600",
		"\xff\xfe invalid utf8",
		strings.Repeat("[", 600),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, garbage := sanitizePDFPageText(s)
		require.True(t, utf8.ValidString(out),
			"sanitized output is not valid UTF-8: %q -> %q", s, out)
		requireNoControlBytesExceptWhitespace(t, out)
		// Empty input is never garbage — the caller relies on that to keep
		// blank pages from poisoning a whole document.
		if s == "" {
			require.False(t, garbage, "the empty page must not be judged garbage")
		}
	})
}

// pdfUndecodable is §31.13's parent-side fail-closed backstop: the child's
// sanitizer means it should never fire on real input, which is exactly why
// it needs a direct test — the coverage profile showed its "this is garbage"
// branch had zero executions in the whole suite, so a backstop that had
// stopped detecting anything would have looked fine.
func TestPDFUndecodableBackstop(t *testing.T) {
	for name, c := range map[string]struct {
		in   string
		want bool
	}{
		"plain prose":                {"ordinary note text", false},
		"whitespace is legitimate":   {"line\nbreak\ttab\rcr", false},
		"replacement runes are text": {"Total: 42� USD", false},
		"empty":                      {"", false},
		"NUL-laced glyph ids":        {"\x00:\x00D\x00Y\x00H", true},
		"pdfium unmapped marker":     {"text \x02 more", true},
		"lone DEL":                   {"text \x7f more", true},
		"escape sequence":            {"\x1b]0;pwned\x07", true},
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			require.Equal(t, c.want, pdfUndecodable(c.in),
				"the §31.6 fail-closed backstop misjudged %q", c.in)
		})
	}
}

// requireNoControlBytesExceptWhitespace is §31.6's "notes are text files"
// ruling as an assertion: only \n, \t and \r may appear below 0x20.
func requireNoControlBytesExceptWhitespace(t *testing.T, s string) {
	t.Helper()
	for i, r := range s {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		require.False(t, r < 0x20 || r == 0x7f,
			"control byte %#x at offset %d of %q", r, i, s)
	}
}
