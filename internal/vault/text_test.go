package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// ScanTextFile must agree byte-for-byte with ScanText, including when a
// CRLF pair or a multibyte rune straddles its 64KiB read-buffer boundary.
func TestScanTextFileMatchesScanText(t *testing.T) {
	boundary := strings.Repeat("a", (64<<10)-1) + "\r\n" // CR is byte 65535, LF is 65536
	cases := map[string]string{
		"clean":               "line\r\ncol\tumn 🎉\n",
		"nul and invalid":     "line\r\nbad\x00byte \xff\n",
		"crlf at buffer edge": boundary + "🎉\x00\n",
	}
	dir := t.TempDir()
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			p := filepath.Join(dir, name+".md")
			if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ScanTextFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if want := ScanText([]byte(src)); got != want {
				t.Errorf("ScanTextFile = %+v, ScanText = %+v", got, want)
			}
		})
	}
	if _, err := ScanTextFile(filepath.Join(dir, "missing.md")); err == nil {
		t.Error("expected an error for a missing file, got nil")
	}
}

func TestScanText(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		invalid     int // expected InvalidUTF8 count
		invalidLine int
		control     int // expected ControlBytes count
		controlLine int
		firstCtrl   byte
	}{
		{name: "empty", src: ""},
		{name: "plain ascii", src: "---\ntitle: ok\n---\n\n# Hi\n"},
		{name: "multibyte ok", src: "emoji 🎉 and CJK 日本語 and é\n"},
		{name: "tab ok", src: "col1\tcol2\n"},
		{name: "crlf ok", src: "line one\r\nline two\r\n"},
		{name: "cr at eof after lf pair", src: "a\r\nb\r\n"},

		{name: "nul byte", src: "before\x00after\n", control: 1, controlLine: 1, firstCtrl: 0x00},
		{name: "nul on later line", src: "one\ntwo\nth\x00ree\n", control: 1, controlLine: 3, firstCtrl: 0x00},
		{name: "del byte", src: "a\x7fb\n", control: 1, controlLine: 1, firstCtrl: 0x7f},
		{name: "vertical tab", src: "a\x0bb\n", control: 1, controlLine: 1, firstCtrl: 0x0b},
		{name: "escape byte", src: "a\x1b[31mred\n", control: 1, controlLine: 1, firstCtrl: 0x1b},
		{name: "bare cr mid line", src: "shown\rhidden\n", control: 1, controlLine: 1, firstCtrl: 0x0d},
		{name: "bare cr at eof", src: "line\r", control: 1, controlLine: 1, firstCtrl: 0x0d},
		{name: "several controls counted", src: "\x00\x01\x02\n", control: 3, controlLine: 1, firstCtrl: 0x00},

		{name: "invalid utf8 single byte", src: "ok \xff bad\n", invalid: 1, invalidLine: 1},
		{name: "invalid utf8 truncated seq", src: "one\ntwo \xe6\x97\n", invalid: 2, invalidLine: 2},
		{name: "invalid and control mixed", src: "\xff\n\x00\n",
			invalid: 1, invalidLine: 1, control: 1, controlLine: 2, firstCtrl: 0x00},

		// C1 controls are valid UTF-8 codepoints and deliberately out of
		// scope (no false positives on odd-but-legal Unicode).
		{name: "c1 control out of scope", src: "a\u0085b\n"},

		// Odd-but-legal Unicode a real vault contains: a BOM from a Windows
		// editor, zero-width joiners in emoji, and U+2028. All valid text \u2014
		// a false positive here would make the check unusable.
		{name: "bom ok", src: "\ufeff---\ntitle: ok\n---\n"},
		{name: "zero width and line separator ok", src: "a\u200bb\u2028c\n"},
		// A literal U+FFFD is a legitimately encoded character, not a decode
		// failure: the scanner must key on the 1-byte RuneError, not the rune.
		{name: "literal replacement char ok", src: "a\ufffdb\n"},

		// Encodings that smuggle a forbidden byte past a naive check. Go
		// rejects each byte of the sequence, so the count is per byte.
		{name: "overlong nul", src: "a\xc0\x80b\n", invalid: 2, invalidLine: 1},
		{name: "surrogate half", src: "a\xed\xa0\x80b\n", invalid: 3, invalidLine: 1},
		{name: "beyond max rune", src: "a\xf4\x90\x80\x80b\n", invalid: 4, invalidLine: 1},
		{name: "invalid byte at eof", src: "abc\xff", invalid: 1, invalidLine: 1},

		// Line accounting around CR: only a CR that is part of CRLF is free,
		// and the LF still moves the line counter.
		{name: "cr cr lf", src: "a\r\r\nb\n", control: 1, controlLine: 1, firstCtrl: 0x0d},
		{name: "control on third crlf line", src: "a\r\nb\r\nc\x00d\r\n", control: 1, controlLine: 3, firstCtrl: 0x00},
		{name: "cr at eof after a full line", src: "a\n\r", control: 1, controlLine: 2, firstCtrl: 0x0d},
		{name: "nul as last byte", src: "abc\x00", control: 1, controlLine: 1, firstCtrl: 0x00},
		// Form feed is a C0 byte: in scope, even though editors sometimes
		// write it as a page break.
		{name: "form feed", src: "a\x0cb\n", control: 1, controlLine: 1, firstCtrl: 0x0c},

		// The two "first" trackers are independent: a control byte before an
		// invalid byte must not shadow the invalid byte's line, or vice versa.
		{name: "control before invalid", src: "\x00\n\xff\n", control: 1, controlLine: 1, firstCtrl: 0x00, invalid: 1, invalidLine: 2},
		{name: "first of many recorded", src: "a\x1bb\x00c\n", control: 2, controlLine: 1, firstCtrl: 0x1b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := ScanText([]byte(tc.src))
			if rep.InvalidUTF8 != tc.invalid {
				t.Errorf("InvalidUTF8 = %d, want %d", rep.InvalidUTF8, tc.invalid)
			}
			if rep.FirstInvalidLine != tc.invalidLine {
				t.Errorf("FirstInvalidLine = %d, want %d", rep.FirstInvalidLine, tc.invalidLine)
			}
			if rep.ControlBytes != tc.control {
				t.Errorf("ControlBytes = %d, want %d", rep.ControlBytes, tc.control)
			}
			if rep.FirstControlLine != tc.controlLine {
				t.Errorf("FirstControlLine = %d, want %d", rep.FirstControlLine, tc.controlLine)
			}
			if rep.FirstControl != tc.firstCtrl {
				t.Errorf("FirstControl = 0x%02X, want 0x%02X", rep.FirstControl, tc.firstCtrl)
			}
			wantOK := tc.invalid == 0 && tc.control == 0
			if rep.OK() != wantOK {
				t.Errorf("OK() = %v, want %v", rep.OK(), wantOK)
			}
		})
	}
}

// A note is scanned once per lint run and once per doctor run, so the
// scanner must not be quadratic in the file it is handed.
func BenchmarkScanText(b *testing.B) {
	src := []byte(strings.Repeat("a line of vault prose with émoji 🎉 and a\ttab\r\n", 200_000))
	b.SetBytes(int64(len(src)))
	for b.Loop() {
		if !ScanText(src).OK() {
			b.Fatal("clean input reported as invalid")
		}
	}
}

// forbiddenByte is an independent, byte-level statement of the §33 rule,
// written without decoding runes. For valid UTF-8 the two formulations must
// agree, because every byte below 0x80 in valid UTF-8 stands alone.
func forbiddenByte(src []byte) bool {
	for i, b := range src {
		switch {
		case b == '\t' || b == '\n':
		case b == '\r':
			if i+1 >= len(src) || src[i+1] != '\n' {
				return true
			}
		case b < 0x20 || b == 0x7F:
			return true
		}
	}
	return false
}

// FuzzScanText pins the contract over arbitrary bytes rather than the cases
// someone thought to write: OK() must agree with "valid UTF-8 and no
// forbidden byte", and every report field must point at something real.
// Seeds carry the encoding edges; `go test -fuzz=FuzzScanText` hunts more.
func FuzzScanText(f *testing.F) {
	for _, s := range []string{
		"", "plain\n", "a\tb\r\nc\r\n", "emoji \U0001F389\n", "\ufeffbom\n",
		"nul\x00", "\x7f", "\x1b[31m", "bare\rcr", "\r\r\n", "\r",
		"\xff", "\xc0\x80", "\xed\xa0\x80", "\xf4\x90\x80\x80", "\xe6\x97",
		"\ufffd", "\u0085", "line\nline\x00\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		src := []byte(s)
		rep := ScanText(src)

		wantOK := utf8.Valid(src) && !forbiddenByte(src)
		if rep.OK() != wantOK {
			t.Fatalf("OK() = %v, want %v for %q (%+v)", rep.OK(), wantOK, s, rep)
		}
		// Counts and their locations must stay consistent: a count of zero
		// means no line, a non-zero count means a real 1-based line.
		if (rep.InvalidUTF8 == 0) != (rep.FirstInvalidLine == 0) {
			t.Fatalf("InvalidUTF8=%d but FirstInvalidLine=%d for %q", rep.InvalidUTF8, rep.FirstInvalidLine, s)
		}
		if (rep.ControlBytes == 0) != (rep.FirstControlLine == 0) {
			t.Fatalf("ControlBytes=%d but FirstControlLine=%d for %q", rep.ControlBytes, rep.FirstControlLine, s)
		}
		lines := 1 + strings.Count(s, "\n")
		if rep.FirstInvalidLine > lines || rep.FirstControlLine > lines {
			t.Fatalf("reported line past end of input (%d lines) for %q: %+v", lines, s, rep)
		}
		// The reported first control byte must actually occur in the input.
		if rep.ControlBytes > 0 {
			if rep.FirstControl >= 0x20 && rep.FirstControl != 0x7F {
				t.Fatalf("FirstControl = 0x%02X is not a control byte for %q", rep.FirstControl, s)
			}
			if !strings.ContainsRune(s, rune(rep.FirstControl)) {
				t.Fatalf("FirstControl = 0x%02X does not occur in %q", rep.FirstControl, s)
			}
		}
		// Scanning is a pure read: it must never mutate the caller's bytes.
		again := ScanText(src)
		if again != rep {
			t.Fatalf("ScanText is not deterministic for %q: %+v then %+v", s, rep, again)
		}
	})
}
