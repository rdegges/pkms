package vault

import "unicode/utf8"

// TextReport summarizes non-text bytes found in a note's raw content
// (SPEC §33). The zero value means the content is valid text.
type TextReport struct {
	InvalidUTF8      int  // count of bytes that are not part of a valid UTF-8 sequence
	FirstInvalidLine int  // 1-based line of the first invalid byte; 0 when none
	ControlBytes     int  // count of forbidden control bytes
	FirstControlLine int  // 1-based line of the first forbidden control byte; 0 when none
	FirstControl     byte // the first forbidden control byte itself
}

// OK reports whether the scanned content is valid text.
func (r TextReport) OK() bool { return r.InvalidUTF8 == 0 && r.ControlBytes == 0 }

// ScanText checks that src is valid UTF-8 and free of control bytes.
// Tab, LF, and CRLF pairs are legal. A bare CR is not — it can visually
// overwrite earlier output in terminals, which is a spoofing vector, and
// no modern editor writes lone-CR line endings. DEL (0x7F) and every
// other C0 byte are forbidden; C1 controls are valid (if odd) Unicode
// and stay out of scope to keep the check false-positive-free.
func ScanText(src []byte) TextReport {
	var rep TextReport
	line := 1
	for i := 0; i < len(src); {
		r, size := utf8.DecodeRune(src[i:])
		if r == utf8.RuneError && size == 1 {
			rep.InvalidUTF8++
			if rep.FirstInvalidLine == 0 {
				rep.FirstInvalidLine = line
			}
			i++
			continue
		}
		switch {
		case r == '\n':
			line++
		case r == '\t':
		case r == '\r' && i+1 < len(src) && src[i+1] == '\n':
			// CRLF: the LF branch counts the line.
		case r < 0x20 || r == 0x7F:
			rep.ControlBytes++
			if rep.FirstControlLine == 0 {
				rep.FirstControlLine = line
				rep.FirstControl = byte(r)
			}
		}
		i += size
	}
	return rep
}
