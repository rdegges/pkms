//go:build panichunt

package ingest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ledongthuc/pdf"
)

// TestHuntPanic sweeps mutations of the golden PDF against the raw
// library to find deterministic panic triggers. Not part of the suite —
// run with -tags panichunt to (re)discover corpus entries.
func TestHuntPanic(t *testing.T) {
	valid := buildMinimalPDF("seed")
	dir := t.TempDir()
	n := 0
	try := func(name string, data []byte) {
		n++
		p := filepath.Join(dir, fmt.Sprintf("x%d.pdf", n))
		_ = os.WriteFile(p, data, 0o644)
		done := make(chan string, 1)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					done <- fmt.Sprintf("PANIC %v", r)
				}
			}()
			f, rd, err := pdf.Open(p)
			if err != nil {
				done <- ""
				return
			}
			defer f.Close()
			for i := 1; i <= rd.NumPage(); i++ {
				pg := rd.Page(i)
				if pg.V.IsNull() {
					continue
				}
				_, _ = pg.GetPlainText(nil)
			}
			done <- ""
		}()
		select {
		case msg := <-done:
			if msg != "" {
				t.Logf("%-30s %s", name, msg)
			}
		case <-time.After(3 * time.Second):
			t.Logf("%-30s HANG", name)
		}
	}
	// Truncations.
	for i := 16; i < len(valid); i += 16 {
		try(fmt.Sprintf("trunc@%d", i), valid[:i])
	}
	// Single-byte corruptions of structural regions.
	for i := 0; i < len(valid); i += 7 {
		m := bytes.Clone(valid)
		m[i] ^= 0xff
		try(fmt.Sprintf("flip@%d", i), m)
	}
	// Structural swaps.
	try("kids-cycle", bytes.Replace(valid, []byte("/Kids [3 0 R]"), []byte("/Kids [2 0 R]"), 1))
	try("root-to-pages-loop", bytes.Replace(valid, []byte("/Pages 2 0 R"), []byte("/Pages 1 0 R"), 1))
	try("count-huge", bytes.Replace(valid, []byte("/Count 1"), []byte("/Count 9"), 1))
	try("contents-to-catalog", bytes.Replace(valid, []byte("/Contents 4 0 R"), []byte("/Contents 1 0 R"), 1))
	try("font-to-page", bytes.Replace(valid, []byte("/F1 5 0 R"), []byte("/F1 3 0 R"), 1))
	try("length-lie", bytes.Replace(valid, []byte("/Length"), []byte("/Xength"), 1))
}
