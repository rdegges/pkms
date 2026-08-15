package vault

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"unicode/utf8"
)

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
// and stay out of scope to keep the check false-positive-free. Line
// numbers are LF-defined, matching Note.LineOf.
func ScanText(src []byte) TextReport {
	// A bytes.Reader can only ever return io.EOF.
	rep, _ := scanReader(bufio.NewReader(bytes.NewReader(src)))
	return rep
}

// ScanTextFile is ScanText for a file on disk, streaming in fixed-size
// chunks — an over-cap (TooLarge, SPEC §14) note is checked without ever
// loading it whole, so size cannot buy an exemption (§33).
func ScanTextFile(path string) (TextReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return TextReport{}, err
	}
	defer func() { _ = f.Close() }()
	return scanReader(bufio.NewReaderSize(f, 64<<10))
}

func scanReader(br *bufio.Reader) (TextReport, error) {
	var rep TextReport
	line := 1
	for {
		r, size, err := br.ReadRune()
		if err == io.EOF {
			return rep, nil
		}
		if err != nil {
			return rep, err
		}
		// ReadRune consumes exactly one byte and reports RuneError with
		// size 1 on an invalid sequence; a literal U+FFFD decodes with
		// size 3, so real replacement characters stay legal.
		if r == utf8.RuneError && size == 1 {
			rep.InvalidUTF8++
			if rep.FirstInvalidLine == 0 {
				rep.FirstInvalidLine = line
			}
			continue
		}
		switch {
		case r == '\n':
			line++
		case r == '\t':
		case r == '\r':
			if next, perr := br.Peek(1); perr == nil && next[0] == '\n' {
				break // CRLF: the LF iteration counts the line
			}
			rep.ControlBytes++
			if rep.FirstControlLine == 0 {
				rep.FirstControlLine = line
				rep.FirstControl = '\r'
			}
		case r < 0x20 || r == 0x7f:
			rep.ControlBytes++
			if rep.FirstControlLine == 0 {
				rep.FirstControlLine = line
				rep.FirstControl = byte(r)
			}
		}
	}
}
