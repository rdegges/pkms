package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
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
		{"pdf", []byte("%PDF-1.5 binary follows"), KindPDF},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0}, KindAsset},
		{"zip", []byte{'P', 'K', 0x03, 0x04, 0, 0, 0, 0}, KindAsset},
		{"plain xml", []byte(`<?xml version="1.0"?><note><to>x</to></note>`), KindAsset},
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

var testTypes = NoteTypes{ProfileName: "para", Clip: "clip", Asset: "asset"}

// noHooks is the push-builder default in tests: no media hooks configured.
var noHooks = MediaHooks{}

type fakeDownloader struct {
	t        *testing.T
	body     []byte
	finalURL string
	header   http.Header
}

func (f fakeDownloader) Download(_ context.Context, rawURL string, _ int64) (*fetch.Download, error) {
	spool := filepath.Join(f.t.TempDir(), "spool")
	if err := os.WriteFile(spool, f.body, 0o600); err != nil {
		return nil, err
	}
	final := f.finalURL
	if final == "" {
		final = rawURL
	}
	h := f.header
	if h == nil {
		h = http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}
	}
	sniff := f.body
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	sum := sha256.Sum256(f.body)
	return &fetch.Download{
		SpoolPath: spool,
		Size:      int64(len(f.body)),
		SHA256:    hex.EncodeToString(sum[:]),
		Sniff:     sniff,
		FinalURL:  final,
		Header:    h,
	}, nil
}

func TestURLRecord(t *testing.T) {
	g := fakeDownloader{t: t, body: []byte(`<html><head><title>Hello World</title></head><body><article><p>Enough article content to extract for the record body goes right here.</p></article></body></html>`)}

	rec, cleanup, err := URLRecord(context.Background(), g, "https://Example.com/post#sec", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "https://example.com/post", rec.NaturalKey, "canonicalized key")
	require.Equal(t, "clip", rec.NoteType)
	require.Equal(t, "https://Example.com/post#sec", rec.Fields["source"], "source stays verbatim")
	require.Equal(t, "Hello World", rec.Fields["title"])
	require.Equal(t, "2026-08-03T12:00:00Z", rec.Fields["created"])
	require.NotContains(t, rec.Fields, "fetched_url", "no redirect → no fetched_url")
	require.Contains(t, rec.Body, "Enough article content")
	require.Empty(t, rec.Assets, "page records carry no assets")
}

func TestURLRecordRecordsRedirect(t *testing.T) {
	g := fakeDownloader{
		t:        t,
		body:     []byte(`<html><head><title>T</title></head><body><article><p>Redirected content body with sufficient length for extraction to work.</p></article></body></html>`),
		finalURL: "https://example.com/final",
	}
	rec, cleanup, err := URLRecord(context.Background(), g, "https://example.com/short", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "https://example.com/short", rec.Fields["source"])
	require.Equal(t, "https://example.com/final", rec.Fields["fetched_url"])
}

func TestURLRecordRejectsNonHTTP(t *testing.T) {
	_, cleanup, err := URLRecord(context.Background(), fakeDownloader{t: t}, "ftp://example.com/x", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.ErrorContains(t, err, "not an http(s) URL")
}

func TestURLRecordAsset(t *testing.T) {
	// The old exit-2 refusal ("unsupported content type … phase 2.5") is
	// gone: a binary type now builds an asset record (SPEC §31.1).
	body := []byte("%PDF-1.5 pretend pdf bytes")
	g := fakeDownloader{t: t, body: body}
	rec, cleanup, err := URLRecord(context.Background(), g, "https://example.com/doc.pdf", testTypes, noHooks, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "https://example.com/doc.pdf", rec.Fields["title"], "remote asset titles are the URL")
	require.Equal(t, "application/pdf", rec.Fields["mime"])
	require.Equal(t, int64(len(body)), rec.Fields["size"])
	require.Len(t, rec.Fields["sha256"], 64)
	require.Equal(t, []any{"asset"}, rec.Fields["tags"])
	require.Len(t, rec.Assets, 1)
	require.Equal(t, "doc.pdf", rec.Assets[0].Filename)
	require.Empty(t, rec.Assets[0].LocalPath, "remote assets never reference in place")

	rc, err := rec.Assets[0].Open()
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, body, got, "asset reader streams the spool")
}

func TestURLRecordAssetWithoutMapping(t *testing.T) {
	g := fakeDownloader{t: t, body: []byte("%PDF-1.5 pretend pdf bytes")}
	nt := NoteTypes{ProfileName: "bare", Clip: "clip"}
	_, cleanup, err := URLRecord(context.Background(), g, "https://example.com/doc.pdf", nt, noHooks, 0, testNow)
	defer cleanup()
	require.ErrorContains(t, err, `profile "bare" declares no asset note type`)
	require.ErrorContains(t, err, `asset = "<note type for ingested binaries>"`)
}

func TestFileRecordText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.md")
	require.NoError(t, os.WriteFile(p, []byte("# My Notes\n\nSome text.\n"), 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err)
	require.Len(t, rec.NaturalKey, 64, "content sha256 hex")
	require.Equal(t, "notes", rec.Fields["title"])
	require.Contains(t, rec.Fields["source"], "file://")
	require.Contains(t, rec.Body, "# My Notes")

	// Same content, different path → same key (content-addressed dedup).
	p2 := filepath.Join(dir, "copy.md")
	require.NoError(t, os.WriteFile(p2, []byte("# My Notes\n\nSome text.\n"), 0o644))
	rec2, err := FileRecord(context.Background(), p2, testTypes, noHooks, testNow)
	require.NoError(t, err)
	require.Equal(t, rec.NaturalKey, rec2.NaturalKey)
}

func TestFileRecordAsset(t *testing.T) {
	dir := t.TempDir()
	body := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 1, 2, 3}
	p := filepath.Join(dir, "shot.png")
	require.NoError(t, os.WriteFile(p, body, 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, noHooks, testNow)
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "shot", rec.Fields["title"], "local asset titles are the filename stem")
	require.Equal(t, "image/png", rec.Fields["mime"])
	require.Equal(t, int64(len(body)), rec.Fields["size"])
	require.Equal(t, rec.NaturalKey, rec.Fields["sha256"], "file pushes key on content hash")
	require.Len(t, rec.Assets, 1)
	require.Equal(t, "shot.png", rec.Assets[0].Filename)
	require.NotEmpty(t, rec.Assets[0].LocalPath, "local files can reference in place")
}

func TestFileRecordMissing(t *testing.T) {
	_, err := FileRecord(context.Background(), filepath.Join(t.TempDir(), "nope.md"), testTypes, noHooks, testNow)
	require.ErrorContains(t, err, "not an http(s) URL and not an existing file")
}

func TestConvertHTMLBareURLDestinationIsClean(t *testing.T) {
	// Regression: smart-mode escaping turned utm_source into utm\_source in
	// a bare-text URL, breaking the link (reported on a real Reddit email).
	// Fixed by linkifying bare URLs → the link DESTINATION is never escaped.
	md, err := ConvertHTML(`<p>See https://ex.com/whats_the_most/?utm_source=share&utm_medium=ios_app here</p>`)
	require.NoError(t, err)
	// The full parenthesized destination carries the URL verbatim — no
	// backslashes — so the link is clickable. (The display label may carry
	// escaped underscores; Obsidian renders those as literal underscores.)
	require.Contains(t, md, "](https://ex.com/whats_the_most/?utm_source=share&utm_medium=ios_app)",
		"link destination is unescaped and clickable")
}

func TestConvertHTMLEscapesLiteralEmphasisInProse(t *testing.T) {
	// The other half: a literal _/* in prose must NOT become italic/bold.
	md, err := ConvertHTML(`<p>Literal _emphasis_ and a*b*c and some_var_name.</p>`)
	require.NoError(t, err)
	require.Contains(t, md, `\_emphasis`, "leading emphasis underscore is escaped")
	require.Contains(t, md, `a\*b\*c`, "asterisks escaped so no bold")
	require.Contains(t, md, `some\_var\_name`, "intraword underscores escaped")
}
