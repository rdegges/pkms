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
	"path/filepath"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"golang.org/x/net/html/charset"

	"github.com/rdegges/pkms/internal/fetch"
)

// Kind is the sniffed dispatch class (SPEC §20).
type Kind int

const (
	KindHTML Kind = iota
	KindText
	KindUnsupported
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
	case strings.HasPrefix(sniffed, "text/xml"):
		probe := body
		if len(probe) > 4096 {
			probe = probe[:4096]
		}
		if bytes.Contains(bytes.ToLower(probe), []byte("<html")) {
			return KindHTML, sniffed
		}
		return KindUnsupported, sniffed
	default:
		return KindUnsupported, sniffed
	}
}

// parseTimeout bounds one document's extraction+conversion (SPEC §21): the
// byte caps make a pathological spin unlikely, but a deadline turns any
// residual catastrophic-regex case into a clean error instead of a hang.
const parseTimeout = 20 * time.Second

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
		m, err := htmltomarkdown.ConvertString(buf.String())
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
	if p := c.Port(); p != "" && !(p == "80" && c.Scheme == "http") && !(p == "443" && c.Scheme == "https") {
		host += ":" + p
	}
	c.Host = host
	c.Fragment = ""
	return c.String()
}

// Getter is the fetch surface URLRecord needs; satisfied by *fetch.Client
// (tests inject canned responses — network policy is fetch's own concern).
type Getter interface {
	Get(ctx context.Context, rawURL string, maxBody int64, conditional http.Header) (*fetch.Result, error)
}

// URLRecord fetches rawURL through the hardened client and builds the push
// record (SPEC §19/§20). noteType comes from the profile's [ingest] table.
func URLRecord(ctx context.Context, c Getter, rawURL, noteType string, now time.Time) (Record, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Record{}, fmt.Errorf("%q is not an http(s) URL; pass a URL or an existing file path", rawURL)
	}
	res, err := c.Get(ctx, rawURL, fetch.MaxHTMLBody, nil)
	if err != nil {
		return Record{}, err
	}
	final, _ := url.Parse(res.FinalURL)
	if final == nil {
		final = u
	}

	rec := Record{
		NaturalKey: CanonicalURL(u),
		NoteType:   noteType,
		Fields: map[string]any{
			"source":  rawURL, // verbatim — never rewritten (SPEC §21)
			"created": now.Format(time.RFC3339),
			"tags":    []any{"clip"},
		},
	}
	if res.FinalURL != rawURL {
		rec.Fields["fetched_url"] = res.FinalURL
	}

	kind, sniffed := Sniff(res.Body)
	switch kind {
	case KindHTML:
		title, md, err := HTMLToMarkdown(res.Body, res.Header.Get("Content-Type"), final)
		if err != nil {
			return Record{}, fmt.Errorf("%s: %w", rawURL, err)
		}
		if title == "" {
			title = rawURL
		}
		rec.Fields["title"] = title
		rec.Body = md
	case KindText:
		rec.Fields["title"] = rawURL
		rec.Body = decodeText(res.Body, res.Header.Get("Content-Type"))
	default:
		return Record{}, unsupportedErr(sniffed)
	}
	return rec, nil
}

// FileRecord builds the push record for a local file (SPEC §20):
// NaturalKey = content SHA-256, source = file:// URL.
func FileRecord(path, noteType string, now time.Time) (Record, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Record{}, err
	}
	fi, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return Record{}, fmt.Errorf("%q is not an http(s) URL and not an existing file", path)
	}
	if err != nil {
		return Record{}, fmt.Errorf("stat %s: %w", abs, err)
	}
	if !fi.Mode().IsRegular() {
		return Record{}, fmt.Errorf("%s is not a regular file", abs)
	}
	if fi.Size() > fetch.MaxHTMLBody {
		return Record{}, fmt.Errorf("%s exceeds the %d MiB ingest limit", abs, fetch.MaxHTMLBody>>20)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return Record{}, err
	}
	sum := sha256.Sum256(body)
	fileURL := &url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}

	rec := Record{
		NaturalKey: hex.EncodeToString(sum[:]),
		NoteType:   noteType,
		Fields: map[string]any{
			"source":  fileURL.String(),
			"created": now.Format(time.RFC3339),
			"tags":    []any{"clip"},
		},
	}
	title := strings.TrimSuffix(filepath.Base(abs), filepath.Ext(abs))
	kind, sniffed := Sniff(body)
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
	default:
		return Record{}, unsupportedErr(sniffed)
	}
	rec.Fields["title"] = title
	return rec, nil
}

func unsupportedErr(sniffed string) error {
	mime := sniffed
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = mime[:i]
	}
	return fmt.Errorf("unsupported content type %s (PDF/audio/video land in phase 2.5); nothing was written", mime)
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
