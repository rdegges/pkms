package ingest

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"github.com/stretchr/testify/require"
)

// buildMinimalPDF renders a valid single-page PDF drawing `text` in
// Helvetica, with a byte-accurate xref — the golden fixture generator
// (SPEC §31.6 "golden small PDFs generated in-repo").
func buildMinimalPDF(text string) []byte {
	esc := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`).Replace(text)
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", esc)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, o := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func writePDF(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "doc.pdf")
	require.NoError(t, os.WriteFile(p, data, 0o644))
	return p
}

// mutate replaces old with new in the golden PDF WITHOUT fixing the xref
// offsets — structural corruption over a valid skeleton, which is what
// reaches the library's panic sites (a broken xref fails cleanly first).
func mutate(valid []byte, old, new string) []byte {
	out := bytes.Replace(valid, []byte(old), []byte(new), 1)
	if bytes.Equal(out, valid) {
		panic("mutation did not apply: " + old)
	}
	return out
}

// hostileCorpus: malformed PDFs that must never crash or hang the binary
// (SPEC §31.6). The panicsRaw entries are OBSERVED to panic the raw
// library (see TestRawLibraryPanicsOnHostileInput and the -tags panichunt
// sweep that found them); "kids self-cycle" is OBSERVED to hang it.
func hostileCorpus() map[string][]byte {
	valid := buildMinimalPDF("seed")
	return map[string][]byte{
		"empty":           []byte("%PDF-1.4\n"),
		"header only":     []byte("%PDF-1.5"),
		"truncated":       valid[:len(valid)/2],
		"no xref":         []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\n%%EOF\n"),
		"lying startxref": []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\nstartxref\n999999\n%%EOF\n"),
		"negative xref":   []byte("%PDF-1.4\nstartxref\n-1\n%%EOF\n"),
		"garbage body":    append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte{0xff, 0x00, 0x7f}, 2048)...),
		// Same-length structural corruptions (xref offsets stay honest, so
		// parsing reaches the corrupted object bodies): raw-panic triggers.
		"objdef becomes int":  mutate(valid, "2 0 obj", "2 0 222"),
		"dict key not a name": mutate(valid, "/Type /Catalog", "0Type /Catalog"),
		"endobj vanishes":     mutate(valid, "endobj\n5 0 obj", "endobX\n5 0 obj"),
	}
}

// hangCorpus: inputs OBSERVED to make the raw library loop forever — the
// §31.6 deadline is the only defense (found by the -tags panichunt sweep).
func hangCorpus() map[string][]byte {
	valid := buildMinimalPDF("seed")
	return map[string][]byte{
		"kids self-cycle": mutate(valid, "/Kids [3 0 R]", "/Kids [2 0 R]"),
	}
}

// TestMain lets this test binary serve as ExtractPDFText's re-exec child
// (the real binary routes through cli.Execute's identical hook).
func TestMain(m *testing.M) {
	if PDFExtractChildMain() {
		return
	}
	os.Exit(m.Run())
}

func TestExtractPDFTextGolden(t *testing.T) {
	p := writePDF(t, buildMinimalPDF("Hello PDF extraction works"))
	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "Hello PDF extraction works")
}

// The RED half of the §31.6 gate: prove the threat is real by observing
// the RAW library panic on at least one corpus entry. If a library
// upgrade ever makes this pass without panicking, the wrapper's recover
// is no longer load-bearing — re-evaluate, don't delete.
func TestRawLibraryPanicsOnHostileInput(t *testing.T) {
	panicked := 0
	for name, data := range hostileCorpus() {
		p := writePDF(t, data)
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked++
					t.Logf("raw pdf.Open/walk panicked on %q: %v", name, r)
				}
			}()
			f, rd, err := pdf.Open(p)
			if err != nil {
				return
			}
			defer func() { _ = f.Close() }()
			for i := 1; i <= rd.NumPage(); i++ {
				pg := rd.Page(i)
				if pg.V.IsNull() {
					continue
				}
				_, _ = pg.GetPlainText(nil)
			}
		}()
	}
	require.Positive(t, panicked, "no corpus entry panicked the raw library — the recover wrapper is untested")
}

// The GREEN half: the wrapper survives the whole corpus — error or text,
// never a panic, never a hang.
func TestExtractPDFTextHostileCorpusNeverPanics(t *testing.T) {
	for name, data := range hostileCorpus() {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			p := writePDF(t, data)
			start := time.Now()
			text, err := ExtractPDFText(p)
			// Well inside the deadline on purpose: a bound above pdfTimeout
			// would let a corpus entry that started hanging pass as green off
			// the timeout path. Entries that hang belong in hangCorpus.
			require.Less(t, time.Since(start), 5*time.Second,
				"no hostile-corpus entry should approach the %s deadline", pdfTimeout)
			if err == nil {
				require.True(t, utf8.ValidString(text), "extracted text must be valid UTF-8, got %q", text)
				require.NotContains(t, text, "\x00")
			}
		})
	}
}

// The deadline half of the §31.6 gate: inputs that loop the raw parser
// forever must come back as clean errors when the deadline fires.
func TestExtractPDFTextDeadlineStopsHangs(t *testing.T) {
	old := pdfTimeout
	pdfTimeout = 2 * time.Second
	t.Cleanup(func() { pdfTimeout = old })

	for name, data := range hangCorpus() {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			p := writePDF(t, data)
			start := time.Now()
			_, err := ExtractPDFText(p)
			require.ErrorContains(t, err, "exceeded")
			require.Less(t, time.Since(start), 10*time.Second)
		})
	}
}

func TestExtractPDFTextEncryptedDegrades(t *testing.T) {
	// A trailer whose /Encrypt points at a nonexistent object: the library
	// rejects the encryption dictionary before any password check, so this
	// exercises the malformed-/Encrypt path, NOT the encrypted-document path
	// (errPDFEncrypted lives in TestExtractPDFTextEncryptedMapsToEncryptedError,
	// which uses a real standard-handler fixture). Pinned to the observed
	// error so the two cases cannot quietly swap places.
	valid := buildMinimalPDF("secret")
	tampered := bytes.Replace(valid,
		[]byte("trailer\n<< /Size 6 /Root 1 0 R >>"),
		[]byte("trailer\n<< /Size 6 /Root 1 0 R /Encrypt 9 0 R >>"), 1)
	require.NotEqual(t, valid, tampered, "fixture tamper must apply")
	p := writePDF(t, tampered)
	_, err := ExtractPDFText(p)
	require.ErrorContains(t, err, "encryption filter")
	require.NotErrorIs(t, err, errPDFEncrypted)
}

func TestPDFBodyNeutralizesEmbeds(t *testing.T) {
	p := writePDF(t, buildMinimalPDF("see ![[secret.png]] inline"))
	body := pdfBody(p)
	require.NotContains(t, body, "![[", "extracted PDF text must not smuggle embeds (§28.9 posture)")
	require.Contains(t, body, "secret.png", "the text itself survives")
}

func TestPDFBodyHintOnGarbage(t *testing.T) {
	p := writePDF(t, []byte("%PDF-1.5\nfake pdf payload\n"))
	body := pdfBody(p)
	require.True(t, strings.HasPrefix(body, "> Text extraction failed: "), "one-line hint, got %q", body)
	require.Equal(t, 1, strings.Count(body, "\n"), "hint is one line")
}

func TestFileRecordPDFExtractsBody(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "paper.pdf")
	require.NoError(t, os.WriteFile(p, buildMinimalPDF("Deterministic body text"), 0o644))

	rec, err := FileRecord(p, testTypes, testNow)
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "application/pdf", rec.Fields["mime"])
	require.Contains(t, rec.Body, "Deterministic body text")
	require.Len(t, rec.Assets, 1, "the PDF is stored as an asset either way")
}

func TestURLRecordPDFExtractsBody(t *testing.T) {
	g := fakeDownloader{t: t, body: buildMinimalPDF("Remote PDF text here")}
	rec, cleanup, err := URLRecord(t.Context(), g, "https://example.com/paper.pdf", testTypes, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Contains(t, rec.Body, "Remote PDF text here")
	require.Len(t, rec.Assets, 1)
}
