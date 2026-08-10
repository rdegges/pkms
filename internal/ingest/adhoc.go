package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"golang.org/x/net/html/charset"

	"github.com/rdegges/pkms/internal/fetch"
)

// Kind is the sniffed dispatch class (SPEC §20/§31.1).
type Kind int

const (
	KindHTML Kind = iota
	KindText
	// KindAsset is the generic fallback: any non-page type lands as an
	// asset note — push mode never refuses a sniffed type (SPEC §31.1).
	KindAsset
	// KindPDF is an asset whose text is extracted into the body (§31.6).
	KindPDF
	// KindMedia is an audio/video asset; its body comes from the §31.7
	// probe_cmd/transcribe_cmd hooks (push mode only).
	KindMedia
)

// Sniff classifies content by its bytes (SPEC §20): DetectContentType over
// the first 512 bytes; the Content-Type header is never trusted for
// dispatch. text/plain is stored VERBATIM (SPEC §20 table) — only XHTML
// served as text/xml, which carries an <html root, is routed to the HTML
// pipeline.
func Sniff(body []byte) (Kind, string) {
	sniffed := http.DetectContentType(body)
	switch {
	case strings.HasPrefix(sniffed, "text/html"):
		return KindHTML, sniffed
	case strings.HasPrefix(sniffed, "text/plain"):
		return KindText, sniffed
	case strings.HasPrefix(sniffed, "application/pdf"):
		return KindPDF, sniffed
	case isMediaMIME(sniffed):
		return KindMedia, sniffed
	case strings.HasPrefix(sniffed, "text/xml"):
		probe := body
		if len(probe) > 4096 {
			probe = probe[:4096]
		}
		if bytes.Contains(bytes.ToLower(probe), []byte("<html")) {
			return KindHTML, sniffed
		}
		return KindAsset, sniffed
	default:
		return KindAsset, sniffed
	}
}

// NoteTypes carries the profile's [ingest] mappings (plus its name, for
// error copy) into the push record builders; the builder picks per sniffed
// kind (SPEC §31.1).
type NoteTypes struct {
	ProfileName string
	Clip        string
	Asset       string
}

func (nt NoteTypes) clipType() (string, error) {
	if nt.Clip == "" {
		return "", fmt.Errorf(`profile %q declares no ingest note type; add to its profile.toml:

  [ingest]
  clip = "<note type for ingested clips>"`, nt.ProfileName)
	}
	return nt.Clip, nil
}

func (nt NoteTypes) assetType() (string, error) {
	if nt.Asset == "" {
		return "", fmt.Errorf(`profile %q declares no asset note type; add to its profile.toml:

  [ingest]
  asset = "<note type for ingested binaries>"`, nt.ProfileName)
	}
	return nt.Asset, nil
}

// parseTimeout bounds one document's extraction+conversion (SPEC §21): the
// byte caps make a pathological spin unlikely, but a deadline turns any
// residual catastrophic-regex case into a clean error instead of a hang.
const parseTimeout = 20 * time.Second

// mdConverter is the shared html-to-markdown converter (goroutine-safe).
// Escaping is SMART (the library/ecosystem default): a literal `_`/`*` in
// body prose is backslash-escaped so it renders as text, not accidental
// emphasis. Smart mode's one failure mode is that it also escapes `_`/`*`
// inside a BARE-TEXT URL (breaking the link) — but the converter never
// escapes a link DESTINATION, so we fix that upstream by linkifying bare
// URLs (linkifyBareURLs) before conversion, turning them into destinations.
// Prose stays safe AND URLs stay intact — see SPEC §30.
var mdConverter = converter.NewConverter(
	converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin()),
	converter.WithEscapeMode(converter.EscapeModeSmart),
)

// ConvertHTML turns an HTML fragment into markdown under parseTimeout. All
// three ingesters (URL, RSS, IMAP) route their html-to-markdown conversion
// through this so §21's parse-deadline promise holds everywhere, not just
// in push mode. A stuck goroutine is bounded by the short-lived process.
func ConvertHTML(html string) (string, error) {
	done := make(chan struct {
		md  string
		err error
	}, 1)
	go func() {
		// linkify runs inside the deadline too (SPEC §21): it parses the
		// full HTML, so it must not be able to hang the caller either.
		md, err := mdConverter.ConvertString(linkifyBareURLs(html))
		done <- struct {
			md  string
			err error
		}{md, err}
	}()
	select {
	case r := <-done:
		return r.md, r.err
	case <-time.After(parseTimeout):
		return "", fmt.Errorf("markdown conversion exceeded %s (content too complex)", parseTimeout)
	}
}

// HTMLToMarkdown converts one fetched HTML document: charset-decode to
// UTF-8 (html-to-markdown does no charset handling), readability
// extraction, then markdown, all under parseTimeout. Returns the extracted
// title ("" when the document has none).
func HTMLToMarkdown(body []byte, contentTypeHeader string, base *url.URL) (title, md string, err error) {
	type result struct {
		title, md string
		err       error
	}
	done := make(chan result, 1)
	go func() {
		utf8r, err := charset.NewReader(bytes.NewReader(body), contentTypeHeader)
		if err != nil {
			done <- result{err: fmt.Errorf("decode charset: %w", err)}
			return
		}
		art, err := readability.FromReader(utf8r, base)
		if err != nil {
			done <- result{err: fmt.Errorf("extract article: %w", err)}
			return
		}
		var buf bytes.Buffer
		if err := art.RenderHTML(&buf); err != nil {
			done <- result{err: fmt.Errorf("render article: %w", err)}
			return
		}
		m, err := ConvertHTML(buf.String())
		if err != nil {
			done <- result{err: fmt.Errorf("convert to markdown: %w", err)}
			return
		}
		done <- result{title: strings.TrimSpace(art.Title()), md: strings.TrimSpace(m) + "\n"}
	}()

	select {
	case r := <-done:
		return r.title, r.md, r.err
	case <-time.After(parseTimeout):
		return "", "", fmt.Errorf("HTML conversion exceeded %s (document too complex)", parseTimeout)
	}
}

// CanonicalURL normalizes a URL into its dedup NaturalKey form (SPEC §20):
// lowercase scheme and host, strip default ports and the fragment — nothing
// else (query params and trailing slashes are meaningful).
func CanonicalURL(u *url.URL) string {
	c := *u
	c.Scheme = strings.ToLower(c.Scheme)
	host := strings.ToLower(c.Hostname())
	if p := c.Port(); p != "" && (p != "80" || c.Scheme != "http") && (p != "443" || c.Scheme != "https") {
		host += ":" + p
	}
	c.Host = host
	c.Fragment = ""
	return c.String()
}

// Getter is the buffered fetch surface (RSS conditional GETs); satisfied
// by *fetch.Client (tests inject canned responses — network policy is
// fetch's own concern).
type Getter interface {
	Get(ctx context.Context, rawURL string, maxBody int64, conditional http.Header) (*fetch.Result, error)
}

// Downloader is the spooled fetch surface URLRecord needs (SPEC §31.3);
// satisfied by *fetch.Client.
type Downloader interface {
	Download(ctx context.Context, rawURL string, maxBody int64) (*fetch.Download, error)
}

// URLRecord fetches rawURL onto a spool through the hardened client and
// builds the push record for the sniffed kind (SPEC §19/§20/§31.1). The
// returned cleanup (never nil) removes the spool; call it only after the
// record's assets have been consumed by the pipeline.
func URLRecord(ctx context.Context, d Downloader, rawURL string, nt NoteTypes, hooks MediaHooks, maxDownload int64, now time.Time) (Record, func(), error) {
	noop := func() {}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Record{}, noop, fmt.Errorf("%q is not an http(s) URL; pass a URL or an existing file path", rawURL)
	}
	dl, err := d.Download(ctx, rawURL, maxDownload)
	if err != nil {
		return Record{}, noop, err
	}
	cleanup := func() { _ = os.Remove(dl.SpoolPath) }
	final, _ := url.Parse(dl.FinalURL)
	if final == nil {
		final = u
	}

	rec := Record{
		NaturalKey: CanonicalURL(u),
		Fields: map[string]any{
			"source":  rawURL, // verbatim — never rewritten (SPEC §21)
			"created": now.Format(time.RFC3339),
		},
	}
	if dl.FinalURL != rawURL {
		rec.Fields["fetched_url"] = dl.FinalURL
	}

	kind, sniffed := Sniff(dl.Sniff)
	if kind == KindAsset || kind == KindPDF || kind == KindMedia {
		rec.NoteType, err = nt.assetType()
		if err != nil {
			cleanup()
			return Record{}, noop, err
		}
		rec.Fields["title"] = rawURL
		fillAssetFields(rec.Fields, sniffed, dl.Size, dl.SHA256)
		switch kind {
		case KindPDF:
			rec.Body = pdfBody(dl.SpoolPath)
		case KindMedia:
			rec.Body = mediaBody(ctx, hooks, dl.SpoolPath)
		}
		rec.Assets = []Asset{{
			Filename: path.Base(final.Path),
			SHA256:   dl.SHA256,
			Size:     dl.Size,
			Open: func() (io.ReadCloser, error) {
				return os.Open(dl.SpoolPath)
			},
		}}
		return rec, cleanup, nil
	}

	// Page kinds are whole-buffered for parsing, so the page cap governs
	// them even when the transfer spooled under the larger download cap
	// (SPEC §21/§31.3).
	defer cleanup()
	if dl.Size > fetch.MaxHTMLBody {
		return Record{}, noop, fmt.Errorf("fetch %s: page body exceeds the size limit (%d MiB)", rawURL, fetch.MaxHTMLBody>>20)
	}
	body, err := os.ReadFile(dl.SpoolPath)
	if err != nil {
		return Record{}, noop, err
	}
	rec.NoteType, err = nt.clipType()
	if err != nil {
		return Record{}, noop, err
	}
	rec.Fields["tags"] = []any{"clip"}
	switch kind {
	case KindHTML:
		title, md, err := HTMLToMarkdown(body, dl.Header.Get("Content-Type"), final)
		if err != nil {
			return Record{}, noop, fmt.Errorf("%s: %w", rawURL, err)
		}
		if title == "" {
			title = rawURL
		}
		rec.Fields["title"] = title
		rec.Body = md
	case KindText:
		rec.Fields["title"] = rawURL
		rec.Body = decodeText(body, dl.Header.Get("Content-Type"))
	}
	return rec, noop, nil
}

// FileRecord builds the push record for a local file (SPEC §20):
// NaturalKey = content SHA-256, source = file:// URL. Page kinds are
// whole-read under the parse cap; asset kinds are streamed and never
// size-capped — the threshold decides placement, not admissibility
// (SPEC §31.3).
func FileRecord(ctx context.Context, fpath string, nt NoteTypes, hooks MediaHooks, now time.Time) (Record, error) {
	abs, err := filepath.Abs(fpath)
	if err != nil {
		return Record{}, err
	}
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return Record{}, fmt.Errorf("%q is not an http(s) URL and not an existing file", fpath)
	}
	if err != nil {
		return Record{}, fmt.Errorf("stat %s: %w", abs, err)
	}
	if !fi.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%s is not a regular file", abs)
	}
	head, err := readHead(abs, 512)
	if err != nil {
		return Record{}, err
	}
	fileURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}

	rec := Record{
		Fields: map[string]any{
			"source":  fileURL.String(),
			"created": now.Format(time.RFC3339),
		},
	}
	title := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	kind, sniffed := Sniff(head)
	// Extension reclassification (LOCAL files only, SPEC §31.1): a
	// container the sniffer misses to octet-stream is routed to the media
	// handler by its proven extension.
	if kind == KindAsset && sniffed == "application/octet-stream" {
		if m := mediaMIMEForLocalExt(abs); m != "" {
			kind, sniffed = KindMedia, m
		}
	}

	if kind == KindAsset || kind == KindPDF || kind == KindMedia {
		rec.NoteType, err = nt.assetType()
		if err != nil {
			return Record{}, err
		}
		sum, err := hashFile(abs)
		if err != nil {
			return Record{}, err
		}
		rec.NaturalKey = sum
		rec.Fields["title"] = title
		fillAssetFields(rec.Fields, sniffed, fi.Size(), sum)
		switch kind {
		case KindPDF:
			rec.Body = pdfBody(abs)
		case KindMedia:
			rec.Body = mediaBody(ctx, hooks, abs)
		}
		rec.Assets = []Asset{{
			Filename: filepath.Base(abs),
			SHA256:   sum,
			Size:     fi.Size(),
			Open: func() (io.ReadCloser, error) {
				return os.Open(abs)
			},
			LocalPath: abs,
		}}
		return rec, nil
	}

	if fi.Size() > fetch.MaxHTMLBody {
		return Record{}, fmt.Errorf("%s exceeds the %d MiB ingest limit", abs, fetch.MaxHTMLBody>>20)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(body)
	rec.NaturalKey = hex.EncodeToString(sum[:])
	rec.NoteType, err = nt.clipType()
	if err != nil {
		return Record{}, err
	}
	rec.Fields["tags"] = []any{"clip"}
	switch kind {
	case KindHTML:
		htitle, md, err := HTMLToMarkdown(body, "", fileURL)
		if err != nil {
			return Record{}, fmt.Errorf("%s: %w", abs, err)
		}
		if htitle != "" {
			title = htitle
		}
		rec.Body = md
	case KindText:
		rec.Body = decodeText(body, "")
	}
	rec.Fields["title"] = title
	return rec, nil
}

// fillAssetFields stamps the asset schema's typed fields (SPEC §31.4).
func fillAssetFields(fields map[string]any, sniffed string, size int64, sha string) {
	mime := sniffed
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	fields["tags"] = []any{"asset"}
	fields["mime"] = strings.TrimSpace(mime)
	fields["size"] = size
	fields["sha256"] = sha
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, n)
	rn, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buf[:rn], err
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func decodeText(body []byte, contentTypeHeader string) string {
	r, err := charset.NewReader(bytes.NewReader(body), contentTypeHeader)
	if err != nil {
		return string(body) // best effort: undecodable → raw bytes
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return string(body)
	}
	s := string(out)
	if !strings.HasSuffix(s, "\n") && s != "" {
		s += "\n"
	}
	return s
}
