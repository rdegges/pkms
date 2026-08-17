package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
// (SPEC §31.6). Historical: several entries were OBSERVED to panic the
// previous engine (ledongthuc/pdf), and "kids self-cycle" to hang it —
// that history is why the recover wrapper and the kill-on-deadline exist.
// The §31.13 engine returns errors for all of them (pinned by
// TestRawEngineSurvivesHostileInput); the corpus stays as the regression
// net for future engine upgrades.
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

// hangCorpusEntry: the input OBSERVED to loop the previous engine forever
// (found by the -tags panichunt sweep). The §31.13 engine parses it in
// milliseconds; it lives on inside hostileCorpus-style coverage below and
// documents why the deadline exists.
func hangCorpusEntry() []byte {
	valid := buildMinimalPDF("seed")
	return mutate(valid, "/Kids [3 0 R]", "/Kids [2 0 R]")
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

// The engine-behavior pin (§31.13 retarget of the old RED-half gate).
// The previous engine, ledongthuc/pdf, was OBSERVED to panic on this
// corpus — that observation justified the recover wrapper. go-pdfium's
// wasm engine returns errors instead; this test pins that: every hostile
// entry must come back as error-or-text with no panic surfacing (a panic
// inside extractPDFInProcess is recovered into a "PDF parser panic"
// error, so the assertion is on the error text). If an engine upgrade
// ever trips this, the recover wrapper caught a real regression —
// re-evaluate, don't delete.
func TestRawEngineSurvivesHostileInput(t *testing.T) {
	for name, data := range hostileCorpus() {
		p := writePDF(t, data)
		text, err := extractPDFInProcess(p)
		if err != nil {
			require.NotContains(t, err.Error(), "PDF parser panic",
				"the engine panicked on %q — the recover wrapper just became load-bearing", name)
			continue
		}
		require.False(t, pdfUndecodable(text), "hostile entry %q yielded control-byte garbage", name)
	}
}

// The GREEN half: the wrapper survives the whole corpus — error or text,
// never a panic, never a hang.
func TestExtractPDFTextHostileCorpusNeverPanics(t *testing.T) {
	for name, data := range hostileCorpus() {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			p := writePDF(t, data)
			start := time.Now()
			text, err := ExtractPDFText(p)
			// An entry that started hanging would ride the deadline and come
			// back "exceeded" — assert the deadline path never fired instead
			// of a tight wall-clock bound: under -race the per-child wasm
			// compile alone takes ~13s, so absolute seconds would measure
			// instrumentation, not the document (see
			// TestExtractPDFTextOldHangTriggerNowParses for the old trigger).
			if err != nil {
				require.NotContains(t, err.Error(), "exceeded",
					"hostile-corpus entry rode the %s deadline", pdfTimeout)
			}
			require.Less(t, time.Since(start), time.Minute,
				"hostile-corpus entry took implausibly long even for -race")
			if err == nil {
				require.True(t, utf8.ValidString(text), "extracted text must be valid UTF-8, got %q", text)
				require.NotContains(t, text, "\x00")
			}
		})
	}
}

// The deadline half of the §31.6 gate: a child that outlives the deadline
// is killed and reported as a clean error. No known input hangs the
// §31.13 engine (the old hang trigger parses in milliseconds — asserted
// below), so the kill path is exercised deterministically instead: the
// per-child wasm compile alone takes ~1.1s, so a deadline shorter than
// that fires on ANY document. Both halves keep the gate honest: the kill
// mechanism works, and the historical trigger stays harmless.
func TestExtractPDFTextDeadlineKillsSlowChild(t *testing.T) {
	old := pdfTimeout
	pdfTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pdfTimeout = old })

	p := writePDF(t, buildMinimalPDF("seed"))
	start := time.Now()
	_, err := ExtractPDFText(p)
	require.ErrorContains(t, err, "exceeded")
	require.Less(t, time.Since(start), 10*time.Second)
}

func TestExtractPDFTextOldHangTriggerNowParses(t *testing.T) {
	p := writePDF(t, hangCorpusEntry())
	_, err := ExtractPDFText(p)
	// NoError is the whole assertion: a re-hang would ride the deadline
	// and come back as an "exceeded" error. No wall-clock bound — under
	// -race the per-child wasm compile alone takes ~13s of wall time.
	require.NoError(t, err,
		"the kids self-cycle must never approach the deadline again")
}

func TestExtractPDFTextMalformedEncryptDictTolerated(t *testing.T) {
	// A trailer whose /Encrypt points at a nonexistent object. The previous
	// engine rejected the document ("encryption filter" error); the §31.13
	// engine tolerates the dangling reference and extracts normally. Pinned
	// so the malformed-/Encrypt path and the real encrypted-document path
	// (TestExtractPDFTextEncryptedMapsToEncryptedError, which uses a real
	// standard-handler fixture) cannot quietly swap places: this document
	// must NEVER map to errPDFEncrypted.
	valid := buildMinimalPDF("secret")
	tampered := bytes.Replace(valid,
		[]byte("trailer\n<< /Size 6 /Root 1 0 R >>"),
		[]byte("trailer\n<< /Size 6 /Root 1 0 R /Encrypt 9 0 R >>"), 1)
	require.NotEqual(t, valid, tampered, "fixture tamper must apply")
	p := writePDF(t, tampered)
	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "secret")
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

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "application/pdf", rec.Fields["mime"])
	require.Contains(t, rec.Body, "Deterministic body text")
	require.Len(t, rec.Assets, 1, "the PDF is stored as an asset either way")
}

func TestURLRecordPDFExtractsBody(t *testing.T) {
	g := fakeDownloader{t: t, body: buildMinimalPDF("Remote PDF text here")}
	rec, cleanup, err := URLRecord(t.Context(), g, "https://example.com/paper.pdf", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Contains(t, rec.Body, "Remote PDF text here")
	require.Len(t, rec.Assets, 1)
}
