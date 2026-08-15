//go:build pdfeval

package ingest

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"
)

// TestPDFEval is the §31.12 readability harness: a MEASUREMENT TOOL, not
// a CI gate. It scores every committed fixture (testdata/pdfeval/) and,
// when present, the maintainer's real corpus
// (.context/pdf-corpus/manifest.json at the repo root) against the frozen
// metric — all 3 ground-truth phrases found after NFKC + lowercase +
// "-\n" hyphenation join + whitespace collapse — and prints a per-document
// PASS/FAIL scorecard with wall times.
//
// Readability failures are the scorecard's DATA, never a test failure:
// with the incumbent ledongthuc/pdf, the word-export and chrome-print
// fixtures are EXPECTED to fail (Identity-H subset fonts — today's
// honest-empty baseline). §31.12's bars judge CANDIDATES at the PR3
// decision gate; this run only fails on contract violations the incumbent
// must already honor: control-byte garbage in output, a no-text fixture
// yielding text, or the encrypted fixture missing the errPDFEncrypted
// hint. A real corpus that is absent prints a loud skip banner and the
// run still passes — but per §31.12 NO candidate is ADOPTABLE without it
// (adoption fails closed; only rejection can be decided from fixtures).
//
// Run it with `make pdf-eval` (the pinned measurement environment).
func TestPDFEval(t *testing.T) {
	fmt.Println("=== §31.12 PDF readability scorecard (extractor: incumbent ledongthuc/pdf) ===")

	// Metric self-check first: a decodable Type1 document the incumbent CAN
	// read (buildMinimalPDF, the §31.6 golden builder), with phrases that
	// only match through the frozen normalization — an NFKC ligature (ﬃ),
	// mixed case, and irregular whitespace. If the meter cannot find these,
	// the harness itself is broken and an all-FAIL scorecard would be a lie
	// about the extractor; that IS a test failure.
	require.Equal(t, "hyphenated word", normalizePDFEvalText("hyphen-\nated word"),
		"the -\\n hyphenation join is part of the frozen metric")
	self := pdfEvalManifestFile{
		File:   "metric-self-check",
		Expect: "text",
		Phrases: []string{
			"eﬃcient meter finds each phrase", // ﬃ: NFKC folds to "ffi"
			"The Efficient Meter Finds",
			"finds  each   phrase after",
		},
	}
	p := writePDF(t, buildMinimalPDF("The efficient meter finds each phrase after folding"))
	if !scorePDFEvalDoc(t, p, self, "") {
		t.Errorf("metric self-check failed — the meter is broken, the scorecard below is untrustworthy")
	}

	pass, total := 0, 0
	for _, f := range loadPDFEvalManifest(t, filepath.Join(pdfEvalDir, "manifest.json")) {
		ok := scorePDFEvalDoc(t, filepath.Join(pdfEvalDir, f.File), f, "")
		total++
		if ok {
			pass++
		}
	}
	fmt.Printf("--- fixtures: %d/%d pass\n", pass, total)

	scoreRealCorpus(t)
}

// scorePDFEvalDoc extracts one document and prints its scorecard line.
// The returned bool is the §31.12 verdict for the document's expect
// class. Only contract violations fail the test (see TestPDFEval).
func scorePDFEvalDoc(t *testing.T, path string, f pdfEvalManifestFile, sum string) bool {
	t.Helper()
	start := time.Now()
	text, err := ExtractPDFText(path)
	wall := time.Since(start).Round(time.Millisecond)

	ok, detail := false, ""
	switch f.Expect {
	case "text":
		found := 0
		normText := normalizePDFEvalText(text)
		for _, p := range f.Phrases {
			if strings.Contains(normText, normalizePDFEvalText(p)) {
				found++
			}
		}
		ok = err == nil && found == len(f.Phrases)
		detail = fmt.Sprintf("phrases=%d/%d", found, len(f.Phrases))
		switch {
		case err != nil:
			detail += " (extraction error: " + err.Error() + ")"
		case text == "":
			detail += " (no extractable text)"
		}
		if err == nil && pdfUndecodable(text) {
			t.Errorf("%s: control-byte garbage in extracted text — §31.6 violation", f.File)
		}
	case "no-text":
		ok = err == nil && text == ""
		detail = "honest no-text"
		switch {
		case err != nil:
			detail = "extraction error: " + err.Error()
		case text != "":
			detail = "yielded text"
			t.Errorf("%s: a no-text document must yield the honest empty outcome, got %d bytes", f.File, len(text))
		}
	case "encrypted":
		ok = errors.Is(err, errPDFEncrypted)
		detail = "errPDFEncrypted hint"
		if !ok {
			detail = fmt.Sprintf("missing encrypted hint (err=%v)", err)
			t.Errorf("%s: the encrypted class must map to errPDFEncrypted, got %v", f.File, err)
		}
	default:
		t.Fatalf("%s: unknown expect %q", f.File, f.Expect)
	}

	verdict := "FAIL"
	if ok {
		verdict = "PASS"
	}
	if sum != "" {
		sum = "  sha256=" + sum
	}
	fmt.Printf("%s  %-20s expect=%-9s %-42s %6s%s\n", verdict, f.File, f.Expect, detail, wall, sum)
	return ok
}

// scoreRealCorpus scores the maintainer's local real corpus the same way,
// printing each document's SHA-256 so the baseline scorecard freezes the
// exact document set a later candidate is judged against (anti-co-tuning).
func scoreRealCorpus(t *testing.T) {
	t.Helper()
	manifestPath := filepath.Join(repoRoot(t), ".context", "pdf-corpus", "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		fmt.Print(`
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
!!
!!  REAL PDF CORPUS ABSENT — SKIPPING
!!  (looked for .context/pdf-corpus/manifest.json at the repo root)
!!
!!  §31.12 FAILS CLOSED: without the real corpus NO candidate
!!  extractor can ever be ADOPTED — only rejection can be decided
!!  from the committed fixtures. Place the real PDFs there with a
!!  manifest.json (same shape as testdata/pdfeval/manifest.json:
!!  file, expect, and 3 ground-truth phrases per text document).
!!
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

`)
		return
	}

	fmt.Println("=== real corpus (.context/pdf-corpus/) ===")
	corpusDir := filepath.Dir(manifestPath)
	pass, textBearing, textPass := 0, 0, 0
	docs := loadPDFEvalManifest(t, manifestPath)
	for _, d := range docs {
		path := filepath.Join(corpusDir, d.File)
		raw, err := os.ReadFile(path)
		require.NoError(t, err, "corpus manifest lists %s but the file is unreadable", d.File)
		ok := scorePDFEvalDoc(t, path, d, fmt.Sprintf("%x", sha256.Sum256(raw)))
		if ok {
			pass++
		}
		if d.Expect == "text" {
			textBearing++
			if ok {
				textPass++
			}
		}
	}
	bar := int(math.Ceil(0.8 * float64(textBearing)))
	fmt.Printf("--- real corpus: %d/%d pass; text-bearing %d/%d readable (adoption bar: ≥%d)\n",
		pass, len(docs), textPass, textBearing, bar)
}

// normalizePDFEvalText applies the frozen §31.12 normalization, in its
// order: Unicode NFKC (folds the ligatures LaTeX loves, e.g. ﬃ → ffi),
// lowercase, join "-\n" line-break hyphenation, collapse whitespace runs
// to single spaces. Phrases get the same treatment before containment.
func normalizePDFEvalText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-\n", "")
	return strings.Join(strings.Fields(s), " ")
}

// repoRoot walks up from the package directory to the go.mod, so the
// harness finds .context/pdf-corpus/ regardless of which directory the
// test binary runs from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above %s", dir)
		dir = parent
	}
}
