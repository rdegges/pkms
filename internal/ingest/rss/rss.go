// Package rss ingests RSS/Atom/JSON feeds (SPEC §22). Dedup is complete
// without a cursor (per-item natural keys); the {etag, last_modified}
// cursor is purely a conditional-GET bandwidth courtesy.
package rss

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"github.com/rdegges/pkms/internal/fetch"
	"github.com/rdegges/pkms/internal/ingest"
)

// MaxItemsPerRun bounds one run against hostile/broken feeds (SPEC §21).
// Uningested overflow is self-healing: items never emitted are never
// acked, so the next run picks them up if the feed still carries them.
const MaxItemsPerRun = 500

func init() {
	ingest.Register("rss", New)
}

// RSS pulls one feed URL.
type RSS struct {
	url      string
	noteType string
	client   ingest.Getter
	nowFn    func() time.Time
	dropped  int
}

// New builds the ingester from its [[vaults.ingesters]] table. Strict
// keys: anything unknown is a config error (SPEC §18).
func New(cfg map[string]any) (ingest.Ingester, error) {
	r := &RSS{client: fetch.New(), nowFn: time.Now}
	for k, v := range cfg {
		switch k {
		case "url":
			s, ok := v.(string)
			if !ok || !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
				return nil, fmt.Errorf("rss: url must be an http(s) URL, got %v", v)
			}
			r.url = s
		case "note_type":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("rss: note_type must be a string")
			}
			r.noteType = s
		default:
			return nil, fmt.Errorf("rss: unknown config key %q (known: url, note_type)", k)
		}
	}
	if r.url == "" {
		return nil, fmt.Errorf(`rss: url is required, e.g. url = "https://example.com/feed.xml"`)
	}
	return r, nil
}

func (r *RSS) Name() string { return "rss" }

// CursorSchema is the on-disk cursor format tag (SPEC §7/§22).
func (r *RSS) CursorSchema() string { return "rss/1" }

// SetNoteType is the pipeline's profile-default hook: config note_type
// wins, else the profile's [ingest] clip type.
func (r *RSS) SetNoteType(t string) {
	if r.noteType == "" {
		r.noteType = t
	}
}

// DroppedItems reports items over MaxItemsPerRun (no silent caps).
func (r *RSS) DroppedItems() int { return r.dropped }

func (r *RSS) Fetch(ctx context.Context, cursor ingest.Cursor, emit ingest.EmitFunc) error {
	cond := http.Header{}
	if etag, _ := cursor["etag"].(string); etag != "" {
		cond.Set("If-None-Match", etag)
	}
	if lm, _ := cursor["last_modified"].(string); lm != "" {
		cond.Set("If-Modified-Since", lm)
	}

	res, err := r.client.Get(ctx, r.url, fetch.MaxFeedBody, cond)
	if err != nil {
		return err
	}
	if res.StatusCode == http.StatusNotModified {
		return nil // clean no-op run; cursor unchanged
	}

	feed, err := gofeed.NewParser().Parse(bytes.NewReader(res.Body))
	if err != nil {
		return fmt.Errorf("parse feed %s: %w", r.url, err)
	}

	items := feed.Items
	if len(items) > MaxItemsPerRun {
		r.dropped = len(items) - MaxItemsPerRun
		items = items[:MaxItemsPerRun]
	}
	fetchTime := r.nowFn().UTC()
	for _, it := range items {
		if it == nil {
			continue
		}
		rec, err := r.record(it, fetchTime)
		if err != nil {
			return err
		}
		if err := emit(ctx, rec); err != nil {
			return err
		}
	}

	// Conditional-GET cursor from the final response — but NOT when items
	// were dropped this run: a 304 next time would starve the overflow
	// forever. Leaving the cursor unadvanced forces a full re-fetch so the
	// dropped items actually get ingested (SPEC §22).
	if r.dropped == 0 {
		if etag := res.Header.Get("ETag"); etag != "" {
			cursor["etag"] = etag
		}
		if lm := res.Header.Get("Last-Modified"); lm != "" {
			cursor["last_modified"] = lm
		}
	}
	return nil
}

func (r *RSS) record(it *gofeed.Item, fetchTime time.Time) (ingest.Record, error) {
	key := naturalKey(r.url, it)

	title := strings.TrimSpace(it.Title)
	if title == "" {
		title = "(untitled)"
	}
	source := it.Link
	if source == "" {
		source = r.url
	}
	created := fetchTime.Format(time.RFC3339)
	if it.PublishedParsed != nil {
		created = it.PublishedParsed.UTC().Format(time.RFC3339)
	}

	fields := map[string]any{
		"title":   title,
		"source":  source, // verbatim item link (SPEC §21)
		"created": created,
		"tags":    []any{"clip", "rss"},
	}
	if authors := authorNames(it); authors != "" {
		fields["author"] = authors
	}

	html := it.Content
	if html == "" {
		html = it.Description
	}
	var body string
	if strings.TrimSpace(html) == "" {
		body = "(the feed provided no content for this item)\n"
	} else {
		md, err := ingest.ConvertHTML(html)
		if err != nil {
			return ingest.Record{}, fmt.Errorf("convert item %q: %w", title, err)
		}
		body = strings.TrimSpace(md) + "\n"
	}

	return ingest.Record{
		NaturalKey: key,
		NoteType:   r.noteType,
		Fields:     fields,
		Body:       body,
	}, nil
}

// naturalKey implements SPEC §22: GUID, else canonical link, else a hash
// of (feed URL, title, published).
func naturalKey(feedURL string, it *gofeed.Item) string {
	if g := strings.TrimSpace(it.GUID); g != "" {
		return g
	}
	if it.Link != "" {
		if u, err := url.Parse(it.Link); err == nil {
			return ingest.CanonicalURL(u)
		}
	}
	sum := sha256.Sum256([]byte(feedURL + "\x00" + it.Title + "\x00" + it.Published))
	return hex.EncodeToString(sum[:])
}

func authorNames(it *gofeed.Item) string {
	var names []string
	for _, a := range it.Authors {
		if a != nil && strings.TrimSpace(a.Name) != "" {
			names = append(names, strings.TrimSpace(a.Name))
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
