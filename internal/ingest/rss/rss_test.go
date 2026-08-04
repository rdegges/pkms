package rss

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/fetch"
	"github.com/rdegges/pkms/internal/ingest"
)

var testNow = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

const feedXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<title>Test Feed</title><link>https://example.com/</link>
<item>
  <title>With GUID</title>
  <link>https://example.com/one</link>
  <guid>urn:example:one</guid>
  <pubDate>Mon, 03 Aug 2026 08:00:00 GMT</pubDate>
  <description>&lt;p&gt;Hello &lt;b&gt;world&lt;/b&gt;&lt;/p&gt;</description>
  <author>a@example.com (Alice Author)</author>
</item>
<item>
  <title>Link Only</title>
  <link>HTTPS://Example.com:443/two#frag</link>
</item>
<item>
  <title>Bare Item</title>
</item>
</channel></rss>`

type fakeGetter struct {
	body   []byte
	status int
	header http.Header
	// gotCond captures the conditional headers of the last request.
	gotCond http.Header
}

func (f *fakeGetter) Get(_ context.Context, rawURL string, _ int64, cond http.Header) (*fetch.Result, error) {
	f.gotCond = cond
	h := f.header
	if h == nil {
		h = http.Header{}
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return &fetch.Result{Body: f.body, FinalURL: rawURL, Header: h, StatusCode: status}, nil
}

func newTestRSS(t *testing.T, g ingest.Getter) *RSS {
	t.Helper()
	ing, err := New(map[string]any{"url": "https://example.com/feed.xml"})
	require.NoError(t, err)
	r := ing.(*RSS)
	r.client = g
	r.nowFn = func() time.Time { return testNow }
	r.SetNoteType("clip")
	return r
}

func collect(t *testing.T, r *RSS, cursor ingest.Cursor) []ingest.Record {
	t.Helper()
	var recs []ingest.Record
	err := r.Fetch(context.Background(), cursor, func(_ context.Context, rec ingest.Record) error {
		recs = append(recs, rec)
		return nil
	})
	require.NoError(t, err)
	return recs
}

func TestFactoryValidation(t *testing.T) {
	_, err := New(map[string]any{})
	require.ErrorContains(t, err, "url is required")

	_, err = New(map[string]any{"url": "ftp://example.com/feed"})
	require.ErrorContains(t, err, "must be an http(s) URL")

	_, err = New(map[string]any{"url": "https://ok.example.com/f", "ur1": "typo"})
	require.ErrorContains(t, err, `unknown config key "ur1"`)
}

func TestFactoryNoteTypeOverrideWins(t *testing.T) {
	ing, err := New(map[string]any{"url": "https://example.com/f", "note_type": "custom"})
	require.NoError(t, err)
	r := ing.(*RSS)
	r.SetNoteType("clip") // profile default must NOT override config
	require.Equal(t, "custom", r.noteType)
}

func TestFetchRecords(t *testing.T) {
	r := newTestRSS(t, &fakeGetter{body: []byte(feedXML)})
	recs := collect(t, r, ingest.Cursor{})
	require.Len(t, recs, 3)

	// GUID wins as the natural key.
	require.Equal(t, "urn:example:one", recs[0].NaturalKey)
	require.Equal(t, "clip", recs[0].NoteType)
	require.Equal(t, "With GUID", recs[0].Fields["title"])
	require.Equal(t, "https://example.com/one", recs[0].Fields["source"])
	require.Equal(t, "2026-08-03T08:00:00Z", recs[0].Fields["created"], "published date, not fetch time")
	require.Equal(t, []any{"clip", "rss"}, recs[0].Fields["tags"])
	require.Equal(t, "Alice Author", recs[0].Fields["author"])
	require.Contains(t, recs[0].Body, "Hello **world**")

	// No GUID → canonicalized link.
	require.Equal(t, "https://example.com/two", recs[1].NaturalKey)
	require.Equal(t, "HTTPS://Example.com:443/two#frag", recs[1].Fields["source"], "source stays verbatim")
	require.Equal(t, "2026-08-03T12:00:00Z", recs[1].Fields["created"], "missing date → fetch time")

	// No GUID, no link → deterministic hash key; stub body.
	require.True(t, strings.HasPrefix(recs[2].NaturalKey, "rss-item:"), recs[2].NaturalKey)
	require.Contains(t, recs[2].Body, "no content for this item")
	require.Equal(t, "https://example.com/feed.xml", recs[2].Fields["source"], "fallback to feed URL")
}

func TestFetchKeysAreStableAcrossRuns(t *testing.T) {
	r1 := newTestRSS(t, &fakeGetter{body: []byte(feedXML)})
	r2 := newTestRSS(t, &fakeGetter{body: []byte(feedXML)})
	a := collect(t, r1, ingest.Cursor{})
	b := collect(t, r2, ingest.Cursor{})
	for i := range a {
		require.Equal(t, a[i].NaturalKey, b[i].NaturalKey, "item %d", i)
	}
}

func TestFetchConditionalGetCursor(t *testing.T) {
	g := &fakeGetter{body: []byte(feedXML), header: http.Header{
		"Etag":          []string{`"v42"`},
		"Last-Modified": []string{"Mon, 03 Aug 2026 08:00:00 GMT"},
	}}
	r := newTestRSS(t, g)
	cursor := ingest.Cursor{}
	collect(t, r, cursor)
	require.Equal(t, `"v42"`, cursor["etag"])
	require.Equal(t, "Mon, 03 Aug 2026 08:00:00 GMT", cursor["last_modified"])

	// Next run sends the conditional headers; 304 emits nothing and keeps
	// the cursor intact.
	g304 := &fakeGetter{status: http.StatusNotModified}
	r2 := newTestRSS(t, g304)
	recs := collect(t, r2, cursor)
	require.Empty(t, recs)
	require.Equal(t, `"v42"`, g304.gotCond.Get("If-None-Match"))
	require.Equal(t, "Mon, 03 Aug 2026 08:00:00 GMT", g304.gotCond.Get("If-Modified-Since"))
	require.Equal(t, `"v42"`, cursor["etag"])
}

func TestFetchCapsItemsPerRun(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><rss version="2.0"><channel><title>big</title>`)
	for i := 0; i < MaxItemsPerRun+7; i++ {
		fmt.Fprintf(&b, `<item><title>i%d</title><guid>g%d</guid></item>`, i, i)
	}
	b.WriteString(`</channel></rss>`)

	r := newTestRSS(t, &fakeGetter{body: []byte(b.String())})
	recs := collect(t, r, ingest.Cursor{})
	require.Len(t, recs, MaxItemsPerRun)
	require.Equal(t, 7, r.DroppedItems(), "cap is reported, never silent")
}

func TestRegisteredInRegistry(t *testing.T) {
	f, err := ingest.Lookup("rss")
	require.NoError(t, err)
	require.NotNil(t, f)
}
