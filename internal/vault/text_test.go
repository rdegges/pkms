package vault

import "testing"

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
