package imap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	skip                 map[uint32]bool   // uid -> reported with nil raw
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
	// UID order, as the protocol guarantees. A UID in skip is reported with
	// nil raw, mirroring realConn's oversized/body-less skip.
	for uid := low; uid < f.uidNext && n < max; uid++ {
		raw, ok := f.msgs[uid]
		if !ok && !f.skip[uid] {
			continue
		}
		if f.skip[uid] {
			raw = nil
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

func TestRecordStoresAttachments(t *testing.T) {
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
	// The attachment now flows to the pipeline as a Record.Asset (SPEC
	// §31.8), which stores it and renders ## Attachments — the record no
	// longer builds that section or claims "not stored".
	require.NotContains(t, rec.Body, "not stored")
	require.NotContains(t, rec.Body, "phase 2.5")
	require.Len(t, rec.Assets, 1, "under-cap attachment is carried as an asset")
	require.NotContains(t, rec.Assets[0].Filename, "/", "no path separator survives")
	require.NotContains(t, rec.Assets[0].Filename, "..", "traversal neutralized in the stored name")
	require.True(t, strings.HasSuffix(rec.Assets[0].Filename, "evil.pdf"), "got %q", rec.Assets[0].Filename)
	require.Len(t, rec.Assets[0].SHA256, 64)

	rc, err := rec.Assets[0].Open()
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	// go-message strips the trailing CRLF (it delimits the MIME boundary).
	require.Equal(t, "%PDF-fake", string(got), "the asset reader yields the decoded bytes")
	require.Equal(t, int64(len(got)), rec.Assets[0].Size, "size matches the stored bytes")
	// The emitter's SHA256 is load-bearing: the assets policy names the CAS
	// blob CASDir/<sha>.ext and dedups in-vault on a hash match. A SHA that
	// disagrees with Open()'s bytes silently corrupts content-addressing, so
	// pin the hash to the actual content, not just its length.
	wantSum := sha256.Sum256(got)
	require.Equal(t, hex.EncodeToString(wantSum[:]), rec.Assets[0].SHA256,
		"SHA256 must be computed over the exact bytes Open() yields")
}

func TestRecordStoresMultipleAttachmentsIndependently(t *testing.T) {
	// Two distinct under-cap attachments. The record loop copies the bytes
	// per iteration (data := a.Data) so each asset's Open closure captures
	// ITS OWN payload — the classic loop-capture bug would make every asset
	// yield the last attachment's bytes. This is the test that proves it,
	// and that each asset's SHA256 matches its own content.
	raw := []byte("Message-Id: <multi@x>\r\n" +
		"From: a@example.com\r\n" +
		"Date: Mon, 03 Aug 2026 08:00:00 +0000\r\n" +
		"Subject: Two attachments\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n\r\nsee both\r\n" +
		"--B\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"first.bin\"\r\n" +
		"\r\nFIRST-payload-alpha\r\n" +
		"--B\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"second.bin\"\r\n" +
		"\r\nsecond-payload-OMEGA-longer\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Len(t, rec.Assets, 2, "both under-cap attachments carried")

	byName := map[string][]byte{
		"first.bin":  []byte("FIRST-payload-alpha"),
		"second.bin": []byte("second-payload-OMEGA-longer"),
	}
	seen := map[string]bool{}
	for _, a := range rec.Assets {
		want, ok := byName[a.Filename]
		require.Truef(t, ok, "unexpected asset filename %q", a.Filename)
		seen[a.Filename] = true

		rc, err := a.Open()
		require.NoError(t, err)
		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		require.NoError(t, rc.Close())
		require.Equal(t, want, got, "asset %q must yield its own bytes, not another part's", a.Filename)

		sum := sha256.Sum256(got)
		require.Equal(t, hex.EncodeToString(sum[:]), a.SHA256, "asset %q SHA256 matches its content", a.Filename)
		require.Equal(t, int64(len(got)), a.Size, "asset %q size matches its content", a.Filename)
	}
	require.Len(t, seen, 2, "the two assets are distinct, not the same part twice")
}

func TestRecordMixedOversizeAndUnderCapAttachments(t *testing.T) {
	// A message carrying one storable and one over-cap attachment: the
	// under-cap part becomes an asset, the over-cap part is listed unstored.
	// Neither is silently dropped (SPEC §23/§31.8).
	big := strings.Repeat("Z", (10<<20)+1)
	raw := []byte("Message-Id: <mixed@x>\r\n" +
		"From: a@example.com\r\n" +
		"Subject: One good one huge\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n\r\nbody\r\n" +
		"--B\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"small.pdf\"\r\n" +
		"\r\n%PDF-small\r\n" +
		"--B\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"huge.bin\"\r\n" +
		"\r\n" + big + "\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)

	require.Len(t, rec.Assets, 1, "only the under-cap attachment is stored")
	require.Equal(t, "small.pdf", rec.Assets[0].Filename)
	rc, err := rec.Assets[0].Open()
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	require.Equal(t, "%PDF-small", string(got))

	// The over-cap part is listed unstored; the storable one is NOT in the
	// body (the pipeline renders ## Attachments from Record.Assets, not here).
	require.Contains(t, rec.Body, "## Attachments not stored")
	require.Contains(t, rec.Body, "huge.bin")
	require.NotContains(t, rec.Body, "small.pdf", "the stored asset is not named in the not-stored list")
	require.NotContains(t, rec.Body, "![[", "the pipeline, not the record, renders the stored-asset embeds")
}

func TestReadAttachmentBoundary(t *testing.T) {
	// The cap is inclusive: exactly maxPartBytes stores, one byte over is
	// oversize (listed unstored, never truncated-and-stored). An empty part
	// is a valid zero-byte asset, not an error.
	atCap := bytes.Repeat([]byte("x"), maxPartBytes)
	data, reason := readAttachment(bytes.NewReader(atCap))
	require.Empty(t, reason, "a part of exactly maxPartBytes is stored, not rejected")
	require.Len(t, data, maxPartBytes)

	overCap := bytes.Repeat([]byte("x"), maxPartBytes+1)
	data, reason = readAttachment(bytes.NewReader(overCap))
	require.Contains(t, reason, "exceeds", "one byte over the cap is oversize")
	require.Nil(t, data, "an oversize part is never partially buffered")

	data, reason = readAttachment(bytes.NewReader(nil))
	require.Empty(t, reason)
	require.Empty(t, data, "an empty attachment is a zero-byte asset, not an error")
}

func TestRecordListsOversizeAttachmentUnstored(t *testing.T) {
	big := strings.Repeat("A", (10<<20)+1) // one byte over the per-part cap
	raw := []byte("Message-Id: <big@x>\r\n" +
		"From: a@example.com\r\n" +
		"Subject: Huge attachment\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n\r\nbody\r\n" +
		"--B\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"big.bin\"\r\n" +
		"\r\n" + big + "\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err)
	require.Empty(t, rec.Assets, "an over-cap attachment is never stored")
	require.Contains(t, rec.Body, "## Attachments not stored")
	require.Contains(t, rec.Body, "big.bin")
	require.Contains(t, rec.Body, "exceeds the 10 MiB per-attachment limit")
}

// A base64 attachment that goes corrupt mid-stream must NEVER be stored as
// a partial asset (that would carry a self-consistent SHA over truncated
// bytes — silent truncation, §23). It is listed unstored with the decode
// reason. (BDFL PR #8 gate condition.)
func TestRecordDecodeErrorAttachmentIsUnstored(t *testing.T) {
	// Valid base64, then a line that is not base64 at all, then more valid
	// base64: go-message's decoder fails partway and io.ReadAll returns the
	// partial bytes plus an error.
	raw := []byte("Message-Id: <corrupt@x>\r\n" +
		"From: a@example.com\r\n" +
		"Subject: Corrupt attachment\r\n" +
		"Content-Type: multipart/mixed; boundary=B\r\n" +
		"\r\n" +
		"--B\r\n" +
		"Content-Type: text/plain\r\n\r\nbody\r\n" +
		"--B\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Disposition: attachment; filename=\"corrupt.bin\"\r\n" +
		"\r\n" +
		"QUJDREVGR0g=\r\n" +
		"!!!not base64 at all!!!\r\n" +
		"SU5WSVNJQkxF\r\n" +
		"--B--\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record(raw, internalDate)
	require.NoError(t, err, "one undecodable part must not fail the whole message")
	require.Empty(t, rec.Assets, "a partially-decoded attachment is never stored")
	require.Contains(t, rec.Body, "## Attachments not stored")
	require.Contains(t, rec.Body, "corrupt.bin")
	require.Contains(t, rec.Body, "could not be decoded")
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

func TestRecordSkipsDeeplyNestedMultipart(t *testing.T) {
	// A deeply-nested multipart message must be skipped without descending
	// the tree (the parse is quadratic) — headers still recorded.
	var b strings.Builder
	b.WriteString("Message-Id: <deep@x>\r\nFrom: a@example.com\r\n")
	b.WriteString("Date: Mon, 03 Aug 2026 08:00:00 +0000\r\nSubject: Deep\r\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "Content-Type: multipart/mixed; boundary=b%d\r\n\r\n--b%d\r\n", i, i)
	}
	b.WriteString("Content-Type: text/plain\r\n\r\nnever reached\r\n")

	m := newTestIMAP(t, nil)
	rec, err := m.record([]byte(b.String()), internalDate)
	require.NoError(t, err)
	require.Equal(t, "deep@x", rec.NaturalKey, "headers still captured")
	require.Contains(t, rec.Body, "nested MIME parts exceed the safe limit")
}

func TestSanitizeAttachmentNameNeutralizesMarkup(t *testing.T) {
	// The security property is what matters: no wikilink/embed/code/table
	// markup, no leading embed marker, no path separators or "..".
	for _, in := range []string{"![[secret.png]]", "[[Now]]", "../../etc/x", "a`code`b", "a|b"} {
		got := sanitizeAttachmentName(in)
		require.NotContains(t, got, "[", "input %q → %q", in, got)
		require.NotContains(t, got, "]", "input %q → %q", in, got)
		require.NotContains(t, got, "`", "input %q → %q", in, got)
		require.NotContains(t, got, "|", "input %q → %q", in, got)
		require.NotContains(t, got, "/", "input %q → %q", in, got)
		require.NotContains(t, got, "..", "input %q → %q", in, got)
		require.False(t, strings.HasPrefix(got, "!"), "input %q → %q", in, got)
	}
	// Exact for the embed case: no `!`, no `[[`.
	require.Equal(t, "--secret.png--", sanitizeAttachmentName("![[secret.png]]"))
}

func TestFetchAdvancesCursorPastSkippedMessage(t *testing.T) {
	// An oversized/body-less message (nil raw) must still advance the
	// cursor so it is never re-fetched forever (SPEC §28.6).
	conn := &fakeConn{uidValidity: 7, uidNext: 4,
		msgs: map[uint32][]byte{1: rawMsg("<a@x>", "One", "body"), 3: rawMsg("<c@x>", "Three", "body")},
		skip: map[uint32]bool{2: true}, // UID 2 is oversized → nil raw
	}
	m := newTestIMAP(t, conn)
	cursor := ingest.Cursor{}
	recs := collect(t, m, cursor)

	require.Len(t, recs, 2, "the two real messages emit; the skipped one does not")
	require.Equal(t, uint64(3), cursor["last_uid"], "cursor advances past the skipped UID 2")
}
