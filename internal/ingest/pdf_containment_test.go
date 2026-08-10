package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// buildPDF assembles objs (1-indexed, in order) into a PDF with a
// byte-accurate xref, so parsing reaches the object bodies instead of
// bailing on a broken table. trailerExtra is appended inside the trailer
// dict (e.g. " /Encrypt 6 0 R"). Companion to buildMinimalPDF in
// pdf_test.go, for fixtures that need more than one page or object.
func buildPDF(objs []string, trailerExtra string) []byte {
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
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, trailerExtra, xref)
	return b.Bytes()
}

// contentStream wraps text in a single Tj so one page extracts to exactly
// that text. text must not contain ( ) or \.
func contentStream(text string) string {
	s := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(s), s)
}

// buildPagesPDF renders one page per entry of texts (obj 1 catalog, obj 2
// page tree, obj 3 font, then page/contents pairs).
func buildPagesPDF(texts []string) []byte {
	kids := make([]string, len(texts))
	for i := range texts {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(texts)),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	for i, txt := range texts {
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R "+
				"/Resources << /Font << /F1 3 0 R >> >> >>", 5+2*i),
			contentStream(txt))
	}
	return buildPDF(objs, "")
}

// sameLenMutate swaps old for new in valid without changing the file
// length, so the xref offsets stay honest and the parser walks into the
// corrupted object rather than failing on the table.
func sameLenMutate(t *testing.T, valid []byte, old, new string) []byte {
	t.Helper()
	require.Len(t, new, len(old), "mutation must preserve file length (xref offsets)")
	out := bytes.Replace(valid, []byte(old), []byte(new), 1)
	require.NotEqual(t, valid, out, "mutation %q did not apply", old)
	return out
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written to the process's real stdout during the call.
func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	os.Stdout = orig
	require.NoError(t, w.Close())
	out := <-done
	require.NoError(t, r.Close())
	return out
}

// --- the 2 MiB cap (SPEC §31.6) -------------------------------------

// A page whose text blows past the cap is truncated in place, marked, and
// still valid UTF-8. The cap path had zero coverage before this test.
func TestExtractPDFTextTruncatesAtCap(t *testing.T) {
	p := writePDF(t, buildPagesPDF([]string{strings.Repeat("Q", 3<<20)}))

	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "[text truncated at the 2 MiB extraction cap]")
	require.True(t, utf8.ValidString(text), "truncation must not cut a rune in half")
	// Body = cap bytes of text plus the one-line marker; nothing near 3 MiB.
	require.LessOrEqual(t, len(text), pdfTextCap+128)
	require.Greater(t, len(text), pdfTextCap-128)
}

// REGRESSION (fixed; pinned): when per-page accumulation lands exactly on
// the cap, the next page computes a negative slice bound and the whole
// extraction is lost to a recovered panic. A valid PDF must yield its text
// (truncated per §31.6) — never zero text plus a "parser panic" hint that
// blames the document.
//
// Reproduce: pdf.go:67 `text[:pdfTextCap-b.Len()]` runs with b.Len() ==
// pdfTextCap+1, because the previous iteration appended len(text)+1 bytes
// after checking only len(text). Fix: clamp the slice (or break when
// b.Len() >= pdfTextCap) instead of subtracting into the negatives.
func TestExtractPDFTextCapBoundaryKeepsText(t *testing.T) {
	const perPage = 16255 // calibrated below; keep in sync with the require
	// One page of N chars extracts to exactly N chars, and each page adds
	// len(text)+1 == N+2 bytes to the accumulator. The boundary needs
	// pages*(N+2) + (N+1) == pdfTextCap exactly, then one more page.
	pages := (pdfTextCap - perPage - 1) / (perPage + 2)
	require.Equal(t, pdfTextCap, pages*(perPage+2)+(perPage+1),
		"fixture is stale: pick a perPage where (perPage+2) divides the cap boundary")

	texts := make([]string, 0, pages+2)
	for i := 0; i < pages+2; i++ {
		texts = append(texts, strings.Repeat("Q", perPage))
	}
	p := writePDF(t, buildPagesPDF(texts))

	text, err := ExtractPDFText(p)
	require.NoError(t, err, "a valid PDF must not fail extraction at the cap boundary")
	require.NotEmpty(t, text, "the cap truncates the text; it never discards all of it")
	require.Contains(t, text, "[text truncated at the 2 MiB extraction cap]")
}

// The single-page calibration the boundary fixture above depends on. If the
// library's page-text shape changes, this fails first and says why.
func TestExtractPDFTextPageTextIsExact(t *testing.T) {
	p := writePDF(t, buildPagesPDF([]string{strings.Repeat("Q", 16255)}))
	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Equal(t, 16255, len(text), "one Tj page must extract to exactly its string")
}

// --- hostile text never becomes live vault markup -------------------

// REGRESSION (fixed; pinned): the success path neutralizes `![[`, but the
// failure path pastes the library's error verbatim — and that error echoes
// bytes straight out of the file. A PDF whose dictionary key is the string
// `(![[x.png]])` puts a live embed in the note body:
//
//	> Text extraction failed: PDF parser panic: unexpected non-name key
//	  string(![[x.png]]) parsing dictionary
//
// Blockquotes do not disable embeds in Obsidian, so this transcludes.
// Fix: run the same neutralization over the hint text in pdfBody, not just
// over the extracted text.
func TestPDFBodyNeutralizesEmbedsOnFailurePath(t *testing.T) {
	valid := buildMinimalPDF("seed")
	// "/Type /Catalog" and "(![[x.png]]) 1" are both 14 bytes.
	hostile := sameLenMutate(t, valid, "/Type /Catalog", "(![[x.png]]) 1")
	p := writePDF(t, hostile)

	body := pdfBody(p)
	require.NotContains(t, body, "![[",
		"no PDF input may put a live embed in a note body, on any path; got %q", body)
}

// REGRESSION (fixed; pinned): github.com/ledongthuc/pdf logs to the
// PROCESS stdout (lex.go:491 `fmt.Printf("DEBUG: %T(%v)\n. Skip dict")`,
// read.go:341/849/862), echoing attacker-controlled bytes. For a CLI whose
// `ingest --json` contract is machine-readable stdout, that is corruption,
// and the echoed bytes are unfiltered (terminal escapes included).
// Fix options: redirect os.Stdout for the duration of the extraction
// goroutine, run extraction in a subprocess, or vendor/patch the library.
func TestExtractPDFTextWritesNothingToProcessStdout(t *testing.T) {
	valid := buildMinimalPDF("seed")
	// 14-byte swap: a non-name dict key carrying raw control bytes.
	hostile := sameLenMutate(t, valid, "/Type /Catalog", "0ZAPPED\x1b\x07\x1b\x07abc")
	p := writePDF(t, hostile)

	var body string
	out := captureStdout(t, func() { body = pdfBody(p) })
	require.NotEmpty(t, body, "the note still gets a hint")
	require.Empty(t, string(out),
		"PDF extraction must not write to the process stdout (`ingest --json` emits there); got %q", out)
}

// --- degradation paths ----------------------------------------------

// A real encrypted PDF (standard handler, wrong /U) must map to the
// dedicated errPDFEncrypted, per §31.6. The pre-existing encryption test
// uses a dangling /Encrypt reference, which fails earlier and never reaches
// this branch — it was 0% covered.
func TestExtractPDFTextEncryptedMapsToEncryptedError(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		contentStream("secret"),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Filter /Standard /V 1 /R 2 /Length 40 /P -1 /O <" + strings.Repeat("41", 32) +
			"> /U <" + strings.Repeat("42", 32) + "> >>",
	}
	const id = "<0102030405060708090a0b0c0d0e0f10>"
	p := writePDF(t, buildPDF(objs, " /Encrypt 6 0 R /ID ["+id+" "+id+"]"))

	text, err := ExtractPDFText(p)
	require.ErrorIs(t, err, errPDFEncrypted)
	require.Empty(t, text, "an encrypted document leaks no plaintext")
	require.Contains(t, pdfBody(p), "encrypted", "the note lands with a one-line hint")
}

// A structurally valid PDF with no text operators gets the dedicated
// "no extractable text" hint, not an error hint (a scanned-image PDF is the
// real-world case).
func TestPDFBodyNoExtractableText(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << >> >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	p := writePDF(t, buildPDF(objs, ""))

	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Empty(t, text)
	require.Equal(t, "> The PDF contains no extractable text.\n", pdfBody(p))
}

// One unreadable page never sinks the document: a page tree that overcounts
// its kids (null page) plus a page whose /Contents is not a stream must
// still yield the good page's text.
func TestExtractPDFTextSurvivesNullAndBrokenPages(t *testing.T) {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 5 0 R] /Count 3 >>",                         // /Count lies: page 3 is null
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 6 0 R >>", // /Contents is a font
		contentStream("good page text"),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 6 0 R >> >> >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	p := writePDF(t, buildPDF(objs, ""))

	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "good page text")
}

// Notes are UTF-8 files: bytes the font encoding cannot map must arrive as
// replacement runes, never as raw invalid bytes.
func TestExtractPDFTextIsAlwaysValidUTF8(t *testing.T) {
	for name, raw := range map[string]string{
		"latin-1 accents": `caf\351 na\357ve`,
		"high bytes":      `\200\201\376\377`,
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			objs := []string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
				"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
					"/Resources << /Font << /F1 5 0 R >> >> >>",
				contentStream(raw),
				"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			}
			p := writePDF(t, buildPDF(objs, ""))

			text, err := ExtractPDFText(p)
			require.NoError(t, err)
			require.True(t, utf8.ValidString(text), "extracted text must be valid UTF-8, got %q", text)
			require.NotContains(t, text, "\x00")
		})
	}
}

// buildCIDPDF renders a page whose font is a subset CID font with
// Identity-H encoding — what Word, InDesign, Chrome's print-to-PDF and
// every other modern producer emits. hexCodes are raw 2-byte glyph ids;
// withToUnicode attaches a correct /ToUnicode CMap mapping them back to
// "Wave teo".
func buildCIDPDF(hexCodes string, withToUnicode bool) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td <%s> Tj ET", hexCodes)
	toUni := ""
	if withToUnicode {
		toUni = " /ToUnicode 8 0 R"
	}
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R " +
			"/Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type0 /BaseFont /AAAAAA+Test /Encoding /Identity-H " +
			"/DescendantFonts [6 0 R]" + toUni + " >>",
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /AAAAAA+Test /CIDSystemInfo " +
			"<< /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor 7 0 R /DW 1000 >>",
		"<< /Type /FontDescriptor /FontName /AAAAAA+Test /Flags 4 /FontBBox [0 0 1000 1000] " +
			"/ItalicAngle 0 /Ascent 1000 /Descent 0 /CapHeight 1000 /StemV 80 >>",
	}
	if withToUnicode {
		cmap := "/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n" +
			"1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
			"4 beginbfchar\n<003A> <0057>\n<0044> <0061>\n<0059> <0076>\n<0048> <0065>\n" +
			"endbfchar\nendcmap\nend\nend"
		objs = append(objs, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap), cmap))
	}
	return buildPDF(objs, "")
}

// REGRESSION (fixed; pinned): the library does not decode subset CID
// fonts. It returns the raw 2-byte glyph ids, so the note body becomes
// NUL-laced binary — `\x00:\x00D\x00Y\x00H` where the document says "Wave".
// Attaching a correct /ToUnicode CMap changes nothing, so this is not a
// malformed-input case: it is the default for modern PDF producers.
//
// Measured on 12 real-world PDFs (`pkms ingest` of each, this branch's
// binary): 9/12 produced glyph-id garbage, 7/12 wrote notes containing NUL
// bytes (up to 14,111 in one 33 KB note), and `grep -I` classifies 7 of the
// 12 notes as binary files. `pkms doctor` reported "10 ok, 0 failures" and
// `pkms lint` reported "clean: no findings" over that vault.
//
// A note is a text file. Whatever the fix (decode /ToUnicode, filter C0
// control bytes, or treat unreadable output as "no extractable text"),
// extraction must not emit control bytes into the vault.
func TestExtractPDFTextNeverEmitsControlBytes(t *testing.T) {
	for name, withToUnicode := range map[string]bool{
		"identity-h subset font":        false,
		"identity-h with ToUnicode map": true,
	} {
		t.Run(strings.ReplaceAll(name, " ", "_"), func(t *testing.T) {
			// Glyph ids for "Wave teo" in a typical subset font.
			p := writePDF(t, buildCIDPDF("003A00440059004800030057004800520003", withToUnicode))

			text, err := ExtractPDFText(p)
			require.NoError(t, err)
			for i, r := range text {
				if r == '\n' || r == '\t' {
					continue
				}
				require.False(t, r < 0x20 || r == 0x7f,
					"control byte %#x at offset %d of extracted text %q — notes are text files", r, i, text)
			}
			require.NotContains(t, pdfBody(p), "\x00", "no NUL byte may reach a note body")
		})
	}
}

// --- record wiring ---------------------------------------------------

// A PDF that fails extraction still lands as a full asset note: type,
// contract fields, stored asset, and the hint body (§31.6 — failure is
// never a refusal).
func TestFileRecordPDFDegradesToAssetNote(t *testing.T) {
	valid := buildMinimalPDF("seed")
	p := writePDF(t, sameLenMutate(t, valid, "/Type /Catalog", "(![[x.png]]) 1"))

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err, "extraction failure must never fail the record")
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "application/pdf", rec.Fields["mime"])
	require.Len(t, rec.Assets, 1, "the PDF is stored either way")
	require.Contains(t, rec.Body, "> Text extraction failed:")
	require.Equal(t, 1, strings.Count(rec.Body, "\n"), "the hint stays one line")
}

// The same for the URL path: a hostile remote PDF degrades, never errors.
func TestURLRecordPDFDegradesToAssetNote(t *testing.T) {
	valid := buildMinimalPDF("seed")
	g := fakeDownloader{t: t, body: sameLenMutate(t, valid, "/Type /Catalog", "(![[x.png]]) 1")}

	rec, cleanup, err := URLRecord(t.Context(), g, "https://example.com/x.pdf", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Contains(t, rec.Body, "> Text extraction failed:")
	require.Len(t, rec.Assets, 1)
}
