package ingest

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/fetch"
)

func TestSniffDispatch(t *testing.T) {
	cases := []struct {
		name string
		body []byte
		want Kind
	}{
		{"html doc", []byte("<!DOCTYPE html><html><body>hi</body></html>"), KindHTML},
		{"html fragment", []byte("<html><p>x</p></html>"), KindHTML},
		{"xhtml with xml decl", []byte(`<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body/></html>`), KindHTML},
		{"plain text", []byte("just some notes\nline two\n"), KindText},
		{"markdown", []byte("# Title\n\nSome *markdown*.\n"), KindText},
		{"pdf", []byte("%PDF-1.5 binary follows"), KindUnsupported},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0}, KindUnsupported},
		{"zip", []byte{'P', 'K', 0x03, 0x04, 0, 0, 0, 0}, KindUnsupported},
		{"plain xml", []byte(`<?xml version="1.0"?><note><to>x</to></note>`), KindUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := Sniff(tc.body)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestCanonicalURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"HTTP://Example.COM/Path", "http://example.com/Path"},
		{"https://example.com:443/a", "https://example.com/a"},
		{"http://example.com:80/a", "http://example.com/a"},
		{"http://example.com:8080/a", "http://example.com:8080/a"},
		{"https://example.com/a#frag", "https://example.com/a"},
		{"https://example.com/a?utm_source=x", "https://example.com/a?utm_source=x"}, // query preserved
		{"https://example.com/a/", "https://example.com/a/"},                         // trailing slash preserved
	}
	for _, tc := range cases {
		u, err := url.Parse(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, CanonicalURL(u), "input %s", tc.in)
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	html := `<!DOCTYPE html><html><head><title>The Article</title></head>
<body><nav>menu junk</nav><article><h1>The Article</h1>
<p>First paragraph with a <a href="/rel">relative link</a>.</p>
<p>Second paragraph.</p></article></body></html>`
	base, _ := url.Parse("https://example.com/post")

	title, md, err := HTMLToMarkdown([]byte(html), "text/html; charset=utf-8", base)
	require.NoError(t, err)
	require.Equal(t, "The Article", title)
	require.Contains(t, md, "First paragraph")
	require.Contains(t, md, "Second paragraph")
	require.Contains(t, md, "https://example.com/rel", "relative links resolve against the base URL")
}

func TestHTMLToMarkdownLatin1(t *testing.T) {
	// ISO-8859-1 bytes: "café" with 0xE9 — html-to-markdown needs UTF-8,
	// so the charset layer must transcode first.
	html := []byte("<html><head><title>caf\xe9</title></head><body><article><p>caf\xe9 content here needs enough text to extract.</p><p>More words to satisfy readability thresholds in this tiny fixture document.</p></article></body></html>")
	base, _ := url.Parse("https://example.com/")

	_, md, err := HTMLToMarkdown(html, "text/html; charset=iso-8859-1", base)
	require.NoError(t, err)
	require.Contains(t, md, "café")
}

type fakeGetter struct {
	body     []byte
	finalURL string
	header   http.Header
}

func (f fakeGetter) Get(_ context.Context, rawURL string, _ int64, _ http.Header) (*fetch.Result, error) {
	final := f.finalURL
	if final == "" {
		final = rawURL
	}
	h := f.header
	if h == nil {
		h = http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
	}
	return &fetch.Result{Body: f.body, FinalURL: final, Header: h, StatusCode: 200}, nil
}

func TestURLRecord(t *testing.T) {
	g := fakeGetter{body: []byte(`<html><head><title>Hello World</title></head><body><article><p>Enough article content to extract for the record body goes right here.</p></article></body></html>`)}

	rec, err := URLRecord(context.Background(), g, "https://Example.com/post#sec", "clip", testNow)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/post", rec.NaturalKey, "canonicalized key")
	require.Equal(t, "clip", rec.NoteType)
	require.Equal(t, "https://Example.com/post#sec", rec.Fields["source"], "source stays verbatim")
	require.Equal(t, "Hello World", rec.Fields["title"])
	require.Equal(t, "2026-08-03T12:00:00Z", rec.Fields["created"])
	require.NotContains(t, rec.Fields, "fetched_url", "no redirect → no fetched_url")
	require.Contains(t, rec.Body, "Enough article content")
}

func TestURLRecordRecordsRedirect(t *testing.T) {
	g := fakeGetter{
		body:     []byte(`<html><head><title>T</title></head><body><article><p>Redirected content body with sufficient length for extraction to work.</p></article></body></html>`),
		finalURL: "https://example.com/final",
	}
	rec, err := URLRecord(context.Background(), g, "https://example.com/short", "clip", testNow)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/short", rec.Fields["source"])
	require.Equal(t, "https://example.com/final", rec.Fields["fetched_url"])
}

func TestURLRecordRejectsNonHTTP(t *testing.T) {
	_, err := URLRecord(context.Background(), fakeGetter{}, "ftp://example.com/x", "clip", testNow)
	require.ErrorContains(t, err, "not an http(s) URL")
}

func TestURLRecordUnsupportedType(t *testing.T) {
	g := fakeGetter{body: []byte("%PDF-1.5 pretend pdf bytes")}
	_, err := URLRecord(context.Background(), g, "https://example.com/doc.pdf", "clip", testNow)
	require.ErrorContains(t, err, "unsupported content type application/pdf")
	require.ErrorContains(t, err, "phase 2.5")
	require.ErrorContains(t, err, "nothing was written")
}

func TestFileRecordText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.md")
	require.NoError(t, os.WriteFile(p, []byte("# My Notes\n\nSome text.\n"), 0o644))

	rec, err := FileRecord(p, "clip", testNow)
	require.NoError(t, err)
	require.Len(t, rec.NaturalKey, 64, "content sha256 hex")
	require.Equal(t, "notes", rec.Fields["title"])
	require.Contains(t, rec.Fields["source"], "file://")
	require.Contains(t, rec.Body, "# My Notes")

	// Same content, different path → same key (content-addressed dedup).
	p2 := filepath.Join(dir, "copy.md")
	require.NoError(t, os.WriteFile(p2, []byte("# My Notes\n\nSome text.\n"), 0o644))
	rec2, err := FileRecord(p2, "clip", testNow)
	require.NoError(t, err)
	require.Equal(t, rec.NaturalKey, rec2.NaturalKey)
}

func TestFileRecordMissing(t *testing.T) {
	_, err := FileRecord(filepath.Join(t.TempDir(), "nope.md"), "clip", testNow)
	require.ErrorContains(t, err, "not an http(s) URL and not an existing file")
}
