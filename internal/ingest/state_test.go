package ingest

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func TestAckSeenPersistence(t *testing.T) {
	p := filepath.Join(t.TempDir(), "imap-work.ndjson")

	s, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	require.False(t, s.Seen("<msg-1@example.com>"))

	require.NoError(t, s.Ack("<msg-1@example.com>", "Resources/Clips/Inbox/x.md", now))
	require.True(t, s.Seen("<msg-1@example.com>"))
	require.NoError(t, s.Quarantine("<bad@example.com>", "schema: missing title", now))
	require.NoError(t, s.SetCursor(Cursor{"uidvalidity": float64(123), "uidnext": float64(456)}, now))
	require.NoError(t, s.Close())

	// Reopen: acks, quarantines and cursor survive (crash recovery = this).
	s2, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()
	require.True(t, s2.Seen("<msg-1@example.com>"), "acked records dedup after restart")
	require.True(t, s2.Seen("<bad@example.com>"), "quarantined records never retry")
	require.False(t, s2.Seen("<msg-2@example.com>"))
	require.Equal(t, float64(456), s2.Cursor()["uidnext"])
}

func TestHeaderWrittenOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.ndjson")
	s, err := OpenState(p, "rss:blog")
	require.NoError(t, err)
	require.NoError(t, s.Close())
	s, err = OpenState(p, "rss:blog")
	require.NoError(t, err)
	require.NoError(t, s.Close())

	raw, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, 1, countLines(raw), "reopening never duplicates the header")
}

func countLines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestCompaction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.ndjson")
	s, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	// A long-lived source accumulates far more cursor lines than keys.
	require.NoError(t, s.Ack("<keep-1@example.com>", "n.md", now))
	require.NoError(t, s.Ack("<keep-2@example.com>", "n.md", now))
	for i := 0; i < compactThreshold+10; i++ {
		require.NoError(t, s.SetCursor(Cursor{"pos": float64(i)}, now))
	}
	require.NoError(t, s.Close())

	s2, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()
	require.Less(t, s2.lines, 10, "log compacted to header + acks + cursor")
	require.True(t, s2.Seen("<keep-1@example.com>"), "seen set survives compaction")
	require.Equal(t, float64(compactThreshold+9), s2.Cursor()["pos"])
}

// A torn final line (crash during append) must not wedge the store: the
// incomplete tail is discarded — its ack never became durable, so the
// record re-fetches and dedups (codex finding).
func TestTornTailRecovers(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.ndjson")
	s, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	require.NoError(t, s.Ack("<ok@example.com>", "n.md", now))
	require.NoError(t, s.Close())

	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(`{"op":"ack","k":"deadbeef`) // torn mid-write
	require.NoError(t, err)
	require.NoError(t, f.Close())

	s2, err := OpenState(p, "imap:work")
	require.NoError(t, err, "torn tail is recoverable, not fatal")
	defer func() { _ = s2.Close() }()
	require.True(t, s2.Seen("<ok@example.com>"), "durable acks survive")
	require.NoError(t, s2.Ack("<next@example.com>", "n2.md", now), "store keeps working")
}

// The state store holds a per-source lock: a second concurrent open fails
// rather than double-ingesting (codex finding).
func TestStateStoreLocked(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.ndjson")
	s, err := OpenState(p, "imap:work")
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	_, err = OpenState(p, "imap:work")
	require.Error(t, err, "second concurrent open is refused")
}

func TestRegistry(t *testing.T) {
	Register("test-src", func(cfg map[string]any) (Ingester, error) { return nil, nil })
	f, err := Lookup("test-src")
	require.NoError(t, err)
	require.NotNil(t, f)
	_, err = Lookup("nope")
	require.ErrorContains(t, err, "unknown ingester")
	require.Contains(t, Registered(), "test-src")
	require.Panics(t, func() {
		Register("test-src", func(cfg map[string]any) (Ingester, error) { return nil, nil })
	})
}
