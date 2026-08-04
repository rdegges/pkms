package imap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/ingest"
)

var internalDate = time.Date(2026, 8, 1, 9, 30, 0, 0, time.UTC)

type fakeConn struct {
	uidValidity, uidNext uint32
	msgs                 map[uint32][]byte // uid -> raw
	fetchedLow           uint32
	fetchCalled          bool
}

func (f *fakeConn) Examine(string) (uint32, uint32, error) {
	return f.uidValidity, f.uidNext, nil
}

func (f *fakeConn) FetchSince(low uint32, max int, fn func(uint32, []byte, time.Time) error) error {
	f.fetchCalled = true
	f.fetchedLow = low
	n := 0
	// UID order, as the protocol guarantees.
	for uid := low; uid < f.uidNext && n < max; uid++ {
		raw, ok := f.msgs[uid]
		if !ok {
			continue
		}
		if err := fn(uid, raw, internalDate); err != nil {
			return err
		}
		n++
	}
	return nil
}

func (f *fakeConn) Close() error { return nil }

func newTestIMAP(t *testing.T, conn *fakeConn) *IMAP {
	t.Helper()
	m, err := New(map[string]any{"host": "imap.example.com", "username": "u@example.com"})
	require.NoError(t, err)
	m.SetNoteType("clip")
	m.SetIdentity("testvault", "mail")
	m.dial = func(context.Context, Config) (Conn, error) { return conn, nil }
	return m
}

func rawMsg(id, subject, body string) []byte {
	var b strings.Builder
	if id != "" {
		b.WriteString("Message-Id: " + id + "\r\n")
	}
	b.WriteString("From: Alice <alice@example.com>\r\n")
	b.WriteString("To: Bob <bob@example.com>\r\n")
	b.WriteString("Date: Mon, 03 Aug 2026 08:00:00 +0000\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(body + "\r\n")
	return []byte(b.String())
}

func collect(t *testing.T, m *IMAP, cursor ingest.Cursor) []ingest.Record {
	t.Helper()
	var recs []ingest.Record
	err := m.Fetch(context.Background(), cursor, func(_ context.Context, r ingest.Record) error {
		recs = append(recs, r)
		return nil
	})
	require.NoError(t, err)
	return recs
}

func TestFactoryValidation(t *testing.T) {
	_, err := New(map[string]any{"username": "u"})
	require.ErrorContains(t, err, "host is required")

	_, err = New(map[string]any{"host": "h", "username": "u", "auth": "plain"})
	require.ErrorContains(t, err, `auth must be "password" or "xoauth2"`)

	_, err = New(map[string]any{"host": "h", "username": "u", "hos": "typo"})
	require.ErrorContains(t, err, `unknown config key "hos"`)

	_, err = New(map[string]any{"host": "h", "username": "u", "password_cmd": "op read x"})
	require.ErrorContains(t, err, "argv array")
	require.ErrorContains(t, err, "never a shell string")
}

func TestFetchFirstRun(t *testing.T) {
	conn := &fakeConn{uidValidity: 7, uidNext: 4, msgs: map[uint32][]byte{
		1: rawMsg("<a@x>", "One", "first"),
		2: rawMsg("<b@x>", "Two", "second"),
		3: rawMsg("<c@x>", "Three", "third"),
	}}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{}
	recs := collect(t, m, cursor)

	require.Len(t, recs, 3)
	require.Equal(t, uint32(1), conn.fetchedLow)
	require.Equal(t, uint64(7), cursor["uidvalidity"])
	require.Equal(t, uint64(3), cursor["last_uid"], "max fetched UID, never UIDNEXT")
	require.False(t, m.CursorWasReset())

	require.Equal(t, "a@x", recs[0].NaturalKey, "angle brackets stripped")
	require.Equal(t, "mid:a@x", recs[0].Fields["source"])
	require.Equal(t, "One", recs[0].Fields["title"])
	require.Equal(t, "2026-08-03T08:00:00Z", recs[0].Fields["created"])
	require.Equal(t, []any{"clip", "email"}, recs[0].Fields["tags"])
	require.Equal(t, []any{`"Alice" <alice@example.com>`}, recs[0].Fields["from"])
	require.Contains(t, recs[0].Body, "first")
}

func TestFetchResume(t *testing.T) {
	conn := &fakeConn{uidValidity: 7, uidNext: 6, msgs: map[uint32][]byte{
		4: rawMsg("<d@x>", "Four", "4"),
		5: rawMsg("<e@x>", "Five", "5"),
	}}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{"uidvalidity": uint64(7), "last_uid": uint64(3)}
	recs := collect(t, m, cursor)

	require.Len(t, recs, 2)
	require.Equal(t, uint32(4), conn.fetchedLow, "resume from last_uid+1")
	require.Equal(t, uint64(5), cursor["last_uid"])
}

func TestFetchNothingNewSkipsTheFetch(t *testing.T) {
	// low >= UIDNEXT → the X:* gotcha would return the LAST message; the
	// spec requires skipping the fetch entirely.
	conn := &fakeConn{uidValidity: 7, uidNext: 4, msgs: map[uint32][]byte{
		3: rawMsg("<c@x>", "Three", "third"),
	}}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{"uidvalidity": uint64(7), "last_uid": uint64(3)}
	recs := collect(t, m, cursor)

	require.Empty(t, recs)
	require.False(t, conn.fetchCalled, "fetch must be skipped entirely")
	require.Equal(t, uint64(3), cursor["last_uid"], "cursor unchanged")
}

func TestFetchUIDValidityChangeResets(t *testing.T) {
	conn := &fakeConn{uidValidity: 99, uidNext: 3, msgs: map[uint32][]byte{
		1: rawMsg("<a@x>", "One", "renumbered"),
		2: rawMsg("<b@x>", "Two", "renumbered"),
	}}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{"uidvalidity": uint64(7), "last_uid": uint64(50)}
	recs := collect(t, m, cursor)

	require.Len(t, recs, 2, "full re-fetch from UID 1 (dedup makes it safe)")
	require.Equal(t, uint32(1), conn.fetchedLow)
	require.True(t, m.CursorWasReset())
	require.Equal(t, uint64(99), cursor["uidvalidity"])
	require.Equal(t, uint64(2), cursor["last_uid"])
}

func TestFetchCursorSurvivesJSONRoundTrip(t *testing.T) {
	// JSON round-trips numbers as float64; the resume math must cope.
	conn := &fakeConn{uidValidity: 7, uidNext: 4, msgs: map[uint32][]byte{}}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{"uidvalidity": float64(7), "last_uid": float64(3)}
	recs := collect(t, m, cursor)
	require.Empty(t, recs)
	require.False(t, conn.fetchCalled)
}

func TestFetchBatchCap(t *testing.T) {
	msgs := map[uint32][]byte{}
	for i := uint32(1); i <= 10; i++ {
		msgs[i] = rawMsg("<m"+strings.Repeat("x", int(i))+"@x>", "S", "b")
	}
	conn := &fakeConn{uidValidity: 7, uidNext: 11, msgs: msgs}
	m, err := New(map[string]any{"host": "h", "username": "u", "batch": int64(4)})
	require.NoError(t, err)
	m.SetNoteType("clip")
	m.dial = func(context.Context, Config) (Conn, error) { return conn, nil }

	cursor := ingest.Cursor{}
	recs := collect(t, m, cursor)
	require.Len(t, recs, 4)
	require.Equal(t, uint64(4), cursor["last_uid"], "cursor advances only over processed UIDs")
}

func TestMessageIDNormalization(t *testing.T) {
	require.Equal(t, "a@x", normalizeMessageID("  <a@x> "))
	require.Equal(t, "A@X", normalizeMessageID("<A@X>"), "case preserved (RFC 5322)")
	require.True(t, usableMessageID("a@x"))
	require.False(t, usableMessageID(""))
	require.False(t, usableMessageID("no-at-sign"))
	require.False(t, usableMessageID(strings.Repeat("x", 999)+"@y"))
}

func TestRecordFallbackKeyWhenMessageIDMissing(t *testing.T) {
	m := newTestIMAP(t, nil)
	raw := rawMsg("", "No ID Here", "identical body")

	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Len(t, rec.NaturalKey, 64, "sha256 fallback key")
	require.True(t, strings.HasPrefix(rec.Fields["source"].(string), "mid:pkms-"), "synthetic mid: source")
	require.True(t, strings.HasSuffix(rec.Fields["source"].(string), "@synthetic.invalid"))

	// Deterministic: same message → same key.
	rec2, err := m.record(rawMsg("", "No ID Here", "identical body"), internalDate)
	require.NoError(t, err)
	require.Equal(t, rec.NaturalKey, rec2.NaturalKey)

	// Different body → different key (templated-bulk defense).
	rec3, err := m.record(rawMsg("", "No ID Here", "different body"), internalDate)
	require.NoError(t, err)
	require.NotEqual(t, rec.NaturalKey, rec3.NaturalKey)
}

func TestRecordMultipartPrefersHTML(t *testing.T) {
	raw := []byte("Message-Id: <mp@x>\r\n" +
		"From: a@example.com\r\n" +
		"Date: Mon, 03 Aug 2026 08:00:00 +0000\r\n" +
		"Subject: =?utf-8?q?Caf=C3=A9_Notes?=\r\n" +
		"Content-Type: multipart/alternative; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"plain version\r\n" +
		"--B\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>html <b>version</b></p>\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Equal(t, "Café Notes", rec.Fields["title"], "RFC 2047 decoded")
	require.Contains(t, rec.Body, "html **version**", "HTML part wins, converted to markdown")
	require.NotContains(t, rec.Body, "plain version")
}

func TestRecordListsAttachments(t *testing.T) {
	raw := []byte("Message-Id: <att@x>\r\n" +
		"From: a@example.com\r\n" +
		"Date: Mon, 03 Aug 2026 08:00:00 +0000\r\n" +
		"Subject: With attachment\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"see attached\r\n" +
		"--B\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"../../etc/evil.pdf\"\r\n" +
		"\r\n" +
		"%PDF-fake\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Contains(t, rec.Body, "## Attachments")
	require.Contains(t, rec.Body, "not stored; attachment support lands in phase 2.5")
	require.NotContains(t, rec.Body, "../", "traversal neutralized in the listed name")
	require.Contains(t, rec.Body, "application/pdf")
}

func TestRecordNoBody(t *testing.T) {
	raw := []byte("Message-Id: <empty@x>\r\nFrom: a@example.com\r\nSubject: Empty\r\n\r\n")
	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Contains(t, rec.Body, "no readable text or HTML body")
	require.Equal(t, "2026-08-01T09:30:00Z", rec.Fields["created"], "INTERNALDATE fallback")
}

func TestXOAUTH2String(t *testing.T) {
	require.Equal(t, "user=u@example.com\x01auth=Bearer tok123\x01\x01",
		xoauth2String("u@example.com", "tok123"))
}

func TestBuildAuthURL(t *testing.T) {
	u := buildAuthURL(defaultAuthURL, "cid", "http://127.0.0.1:1234/")
	require.Contains(t, u, "accounts.google.com")
	require.Contains(t, u, "client_id=cid")
	require.Contains(t, u, "access_type=offline")
	require.Contains(t, u, "prompt=consent")
	require.Contains(t, u, "scope=https%3A%2F%2Fmail.google.com%2F")
}

func TestRegisteredInRegistry(t *testing.T) {
	f, err := ingest.Lookup("imap")
	require.NoError(t, err)
	require.NotNil(t, f)
}
