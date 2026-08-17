package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"

	"github.com/rdegges/pkms/internal/paths"
)

// pdfTextCap bounds extracted text (SPEC §31.6), enforced DURING per-page
// accumulation — a decompression bomb must hit the cap as it inflates,
// never balloon unbounded before a post-hoc trim.
const pdfTextCap = 2 << 20

// pdfTimeout is §31.6's extraction deadline. Extraction runs in a child
// process the parent KILLS on timeout — real containment: no abandoned
// goroutine burning CPU, and a wedged parser can never print after the
// parent has moved on. A var so the hang tests can shorten it.
var pdfTimeout = 20 * time.Second

var errPDFEncrypted = errors.New("PDF is encrypted; text extraction skipped")

// Extraction runs out-of-process regardless of engine (SPEC §31.12: the
// child lattice is containment independent of extractor internals). The
// kill-on-deadline is real hang containment no in-process guard matches,
// stdout/stderr → /dev/null holds on every path including timeout-kill,
// and a parser crash or memory blowup dies in the child, never the
// ingest run. go-pdfium's wazero sandbox is defense-in-depth on top,
// never a substitute. (Historical: the previous engine, ledongthuc/pdf,
// also printed attacker-controlled bytes to stdout mid-parse — the
// original reason this lattice exists.)
const (
	pdfChildEnv      = "PKMS_PDF_EXTRACT_CHILD"
	pdfExitEncrypted = 3
	pdfExitError     = 4
	// pdfChildSentinel is the fixed argv[1] of a real extraction child.
	// No pkms subcommand equals it, so a normal invocation — with ANY
	// value of pdfChildEnv inherited — is never mistaken for a child and
	// can never have argv[2] truncated (the clobber the PR #6 gate found).
	pdfChildSentinel = "__pkms_pdf_extract_child__"
)

// PDFExtractChildMain is the child-process entry point. Callers (cli.
// Execute and the package's TestMain) invoke it first. A child is
// selected ONLY by the fixed argv sentinel; once selected it is
// authenticated by a per-run random nonce (argv[2] must equal the env
// value the parent set) so a stray sentinel alone can never drive a
// write. Returns false when this is not a child invocation.
func PDFExtractChildMain() bool {
	if len(os.Args) < 2 || os.Args[1] != pdfChildSentinel {
		return false // not a child — normal CLI/test run, whatever the env
	}
	// From here we KNOW a child was intended; never fall through to normal
	// behavior (that would run the whole CLI/suite). Authenticate strictly:
	// argv = [exe, sentinel, nonce, input, output], env nonce must match.
	nonce := os.Getenv(pdfChildEnv)
	if nonce == "" || len(os.Args) != 5 || os.Args[2] != nonce {
		os.Exit(pdfExitError)
	}
	inPath, outPath := os.Args[3], os.Args[4]
	text, err := extractPDFInProcess(inPath)
	switch {
	case errors.Is(err, errPDFEncrypted):
		os.Exit(pdfExitEncrypted)
	case err != nil:
		_ = os.WriteFile(outPath, []byte(err.Error()), 0o600)
		os.Exit(pdfExitError)
	}
	if err := os.WriteFile(outPath, []byte(text), 0o600); err != nil {
		os.Exit(pdfExitError)
	}
	os.Exit(0)
	return true // unreachable
}

// extractPDFInProcess is the raw extraction via go-pdfium/webassembly
// (adopted by measurement — SPEC §31.13; the ledongthuc/pdf engine it
// replaces read ~3/9 of the real corpus). It runs ONLY in the child, so
// the wazero compile+instantiate of the embedded pdfium.wasm is paid per
// extraction — measured ~1.1s, inside the §31.12 latency budget. The
// recover() is defense-in-depth (the wasm engine returns errors rather
// than panicking, but the previous engine taught us not to rely on it),
// and the §31.6 cap is enforced as pages accumulate.
func extractPDFInProcess(path string) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", fmt.Errorf("PDF parser panic: %v", r)
		}
	}()
	pool, err := initPDFiumPool()
	if err != nil {
		return "", err
	}
	defer func() { _ = pool.Close() }()
	inst, err := pool.GetInstance(pdfTimeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = inst.Close() }()

	doc, err := inst.OpenDocument(&requests.OpenDocument{FilePath: &path})
	if err != nil {
		// ErrPassword = a user password is required; ErrSecurity = an
		// unsupported protection scheme. Both mean "encrypted, cannot
		// extract" — the §31.6 hint, never a garbage body.
		if errors.Is(err, pdfiumerrors.ErrPassword) || errors.Is(err, pdfiumerrors.ErrSecurity) {
			return "", errPDFEncrypted
		}
		return "", err
	}
	defer func() {
		_, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	}()

	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", err
	}

	var b strings.Builder
	truncated := false
	for i := 0; i < pc.PageCount && !truncated; i++ {
		res, perr := inst.GetPageText(&requests.GetPageText{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if perr != nil {
			continue // one broken page never sinks the document
		}
		// Per-page garbage screen, refined for this engine (§31.13):
		// pdfium marks a glyph it cannot map to text as U+0002, so a page
		// of real prose can carry a few control runes (math symbols,
		// exotic ligatures). Those become U+FFFD — visible, honest, never
		// raw control bytes in a note. A page that is MOSTLY control
		// runes is undecoded glyph-id output and is dropped whole, same
		// as the §31.6 ruling always demanded. Both halves of that ruling
		// hold: no garbage in a body, and never "no text" over real text.
		pageText, garbage := sanitizePDFPageText(res.Text)
		if garbage {
			continue
		}
		if b.Len() >= pdfTextCap {
			truncated = true
			break
		}
		if b.Len()+len(pageText) > pdfTextCap {
			pageText = strings.ToValidUTF8(pageText[:pdfTextCap-b.Len()], "")
			truncated = true
		}
		b.WriteString(pageText)
		b.WriteString("\n")
	}
	out := strings.TrimSpace(b.String())
	if truncated {
		out += "\n\n[text truncated at the 2 MiB extraction cap]"
	}
	return out, nil
}

// initPDFiumPool builds the child's engine pool, with a persistent
// wazero compilation cache when one can be opened (SPEC §31.14). The
// cache turns the per-child ~1.1s wasm compile into a ~0.1s load; every
// cache problem is only the slow path, never a failure:
//   - cache dir unusable (not creatable, a file, wrong perms) → compile
//     without a cache, exactly the pre-§31.14 behavior;
//   - Init fails WITH a cache configured (e.g. a poisoned/corrupt entry
//     the engine cannot deserialize) → retry once without the cache.
//
// On-disk contents are wazero-version-scoped by wazero itself and keyed
// by module content, and entries are written atomically (temp + fsync +
// rename), so concurrent extraction children share the dir safely.
func initPDFiumPool() (pdfium.Pool, error) {
	cfg := webassembly.Config{MinIdle: 1, MaxIdle: 1, MaxTotal: 1}
	if cache, err := wazero.NewCompilationCacheWithDir(paths.CacheDir("wazero")); err == nil {
		cfg.RuntimeConfig = wazero.NewRuntimeConfig().WithCompilationCache(cache)
		if pool, err := webassembly.Init(cfg); err == nil {
			return pool, nil
		}
		cfg.RuntimeConfig = nil
	}
	return webassembly.Init(cfg)
}

// ExtractPDFText extracts the plain text of the PDF at path under the
// §31.6 containment lattice, via the child process described above. The
// result is always valid UTF-8; output the engine could not actually
// decode into text is reported as empty: "no extractable text" is the
// truthful answer, binary garbage in a note body never is. With the
// §31.13 engine, CID/Identity-H subset fonts (Word, Google Docs, pdfTeX
// producers) decode — the committed pdfeval fixtures assert it in CI.
func ExtractPDFText(path string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	nonce, err := randomNonce()
	if err != nil {
		return "", err
	}
	out, err := os.CreateTemp("", "pkms-pdftext-*")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	_ = out.Close()
	defer func() { _ = os.Remove(outPath) }()

	ctx, cancel := context.WithTimeout(context.Background(), pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, pdfChildSentinel, nonce, path, outPath)
	cmd.Env = append(os.Environ(), pdfChildEnv+"="+nonce)
	// Stdout/Stderr stay nil → /dev/null: the library's debug prints die
	// there on every path, including after a timeout kill.
	runErr := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("PDF text extraction exceeded %s; the document may be malformed", pdfTimeout)
	}
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			switch ee.ExitCode() {
			case pdfExitEncrypted:
				return "", errPDFEncrypted
			case pdfExitError:
				if msg, rerr := os.ReadFile(outPath); rerr == nil && len(msg) > 0 {
					return "", errors.New(string(msg))
				}
				return "", errors.New("PDF text extraction failed")
			}
		}
		return "", fmt.Errorf("PDF text extraction: %w", runErr)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return "", err
	}
	text := strings.ToValidUTF8(string(raw), "�")
	if pdfUndecodable(text) {
		return "", nil
	}
	return text, nil
}

// randomNonce is the per-run child-auth secret (SPEC §31.6): 16 bytes of
// crypto/rand, hex. An attacker who sets pdfChildEnv cannot guess it, so
// even a stray sentinel in argv cannot pass authentication.
func randomNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// sanitizePDFPageText prepares one page of engine output (§31.13):
// control runes other than \n/\t/\r — pdfium's marker for glyphs with no
// text mapping is U+0002 — become U+FFFD, and the page is reported as
// garbage when more than 10% of its runes were control bytes: that ratio
// separates undecoded glyph-id output (historically ~all control bytes)
// from real prose with isolated unmappable symbols (measured ~0.6% on
// arXiv pdfTeX papers). The returned text contains no control bytes by
// construction.
func sanitizePDFPageText(s string) (string, bool) {
	total, ctrl := 0, 0
	out := strings.Map(func(r rune) rune {
		total++
		if r == '\n' || r == '\t' || r == '\r' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			ctrl++
			return '�'
		}
		return r
	}, s)
	if total == 0 {
		return "", false
	}
	return out, ctrl*10 > total
}

// pdfUndecodable reports control-byte-laced output — the signature of
// glyph ids an engine returned undecoded. Such "text" must never land
// in a note body; notes are text files (§31.6 honesty ruling). It backs
// the parent-side fail-closed check in ExtractPDFText (the child's
// sanitizer means it should never fire there — defense in depth). \n,
// \t and \r are legitimate whitespace, not garbage (matching
// neutralizePDFText's treatment of \r — resolved deliberately in one
// direction).
func pdfUndecodable(text string) bool {
	for _, r := range text {
		if r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// pdfBody renders a PDF record's note body: the extracted text, or a
// one-line hint when extraction fails (SPEC §31.6 — failure degrades to a
// plain asset note; the PDF itself is stored either way). BOTH paths are
// neutralized: extracted text AND parser errors echo bytes out of a
// hostile file, and neither may mint embeds (`![[`) or graph edges (`[[`)
// in the vault (§28.9 posture; BDFL ruling at the PR #6 gate).
func pdfBody(path string) string {
	text, err := ExtractPDFText(path)
	switch {
	case err != nil:
		return "> Text extraction failed: " + neutralizePDFHint(err.Error()) + "\n"
	case text == "":
		return "> The PDF contains no extractable text.\n"
	default:
		return neutralizePDFText(text) + "\n"
	}
}

// pdfHintCap bounds the failure hint (SPEC §31.6): the library echoes file
// bytes into its errors, so an uncapped hint lets a hostile PDF size the
// note. A diagnostic never needs more than this.
const pdfHintCap = 512

// escapeBrackets escapes EVERY `[`, not just `[[` pairs. Escaping each
// opener structurally guarantees the output can never contain two adjacent
// unescaped brackets — closing the odd-run and backslash-doubling bypasses
// the PR #6 gate found — while `\[12\]` still renders as `[12]`, so
// citations survive (BDFL ruling: neither embeds `![[` nor graph edges
// `[[` may be minted from a hostile PDF).
func escapeBrackets(s string) string {
	return strings.ReplaceAll(s, "[", `\[`)
}

// neutralizePDFText strips control bytes and escapes wikilink/embed
// openers. \n and \t pass through; a CRLF pair collapses to \n FIRST
// (the §31.13 engine emits \r\n between text runs — mapping its \r to a
// space would end every extracted line in " \n": diff noise in a synced
// vault and an accidental Markdown hard break); a bare \r then becomes
// a space so a CR-delimited line break never glues two words together
// (this is also what pdfUndecodable treats \r as — the two agree
// deliberately).
func neutralizePDFText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	return escapeBrackets(s)
}

// neutralizePDFHint is the one-line, bounded form: newlines, tabs and \r
// collapse to spaces, control bytes drop, the result is capped at
// pdfHintCap bytes (rune-safe), then brackets are escaped.
func neutralizePDFHint(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
	if len(s) > pdfHintCap {
		cut := pdfHintCap
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return escapeBrackets(s)
}
