package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/profile"
)

var now = time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)

func meetingFields() map[string]any {
	return map[string]any{
		"date": "2026-05-06", "time": "11:00 - 12:00", "duration": int64(60),
		"type": "meeting", "has_transcript": false,
		"attendees": []any{"[[Jane Doe]]"}, "tags": []any{"meeting", "snyk"},
		"category": "Snyk", "hhmm": "1100", "title": "Weekly Sync",
	}
}

func TestWriteValidRecord(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	root := t.TempDir()
	q := filepath.Join(t.TempDir(), "failed")

	rel, err := Write(root, prof, "meeting", meetingFields(), "## Summary\nHi.", q, "", now)
	require.NoError(t, err)
	require.Equal(t, "Meetings/Snyk/2026/05/06/1100 - Weekly Sync.md", rel)

	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	require.Contains(t, string(raw), "date: \"2026-05-06\"")
	require.Contains(t, string(raw), "## Summary")

	// Collision → deterministic suffix, never overwrite.
	rel2, err := Write(root, prof, "meeting", meetingFields(), "other", q, "", now)
	require.NoError(t, err)
	require.Equal(t, "Meetings/Snyk/2026/05/06/1100 - Weekly Sync 2.md", rel2)
}

func TestWriteInvalidRecordQuarantines(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	root := t.TempDir()
	q := filepath.Join(t.TempDir(), "failed")

	bad := meetingFields()
	delete(bad, "date") // required by schema AND placement template

	_, err = Write(root, prof, "meeting", bad, "body", q, "", now)
	require.ErrorIs(t, err, ErrQuarantined)

	entries, err := os.ReadDir(q)
	require.NoError(t, err)
	require.Len(t, entries, 1, "record landed in quarantine (outside the vault)")

	var vaultFiles int
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			vaultFiles++
		}
		return nil
	})
	require.Zero(t, vaultFiles, "nothing malformed ever lands in the vault")
}

func TestWriteSymlinkEscapeRefused(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	root := t.TempDir()
	outside := t.TempDir()

	// Meetings/Snyk symlinked outside the vault.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Meetings"), 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "Meetings", "Snyk")))

	_, err = Write(root, prof, "meeting", meetingFields(), "body", filepath.Join(t.TempDir(), "q"), "", now)
	require.ErrorContains(t, err, "escapes the vault")

	entries, _ := os.ReadDir(outside)
	require.Empty(t, entries, "nothing written through the symlink")
}

func clipProfile(t *testing.T) *profile.Profile {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(`
schema_version = 1
name = "t"
description = "t"
scaffold = ["Inbox"]

[[types]]
name = "clip"
scope = ["Inbox/*.md"]
schema = "schemas/clip.schema.json"
folder = "Inbox"
filename = "{{.title}}"
`), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "schemas"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "schemas", "clip.schema.json"), []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["title"],
  "properties": {"title": {"type": "string"}}
}`), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err)
	return prof
}

// A title the YAML emitter can't round-trip ("? x" → zero fields) must be
// quarantined, never written as silently-corrupt frontmatter.
func TestWriteQuarantinesRoundTripBreakingTitle(t *testing.T) {
	prof := clipProfile(t)
	root := t.TempDir()
	q := filepath.Join(t.TempDir(), "failed")
	for _, title := range []string{"? x", ".inf", ".nan"} {
		_, err := Write(root, prof, "clip", map[string]any{"title": title}, "body", q, "k"+title, now)
		require.ErrorIs(t, err, ErrQuarantined, "title %q", title)
	}
}

// An over-long title truncates to a valid note rather than failing.
func TestWriteTruncatesLongTitle(t *testing.T) {
	prof := clipProfile(t)
	root := t.TempDir()
	q := filepath.Join(t.TempDir(), "failed")
	rel, err := Write(root, prof, "clip", map[string]any{"title": strings.Repeat("A", 400)}, "b", q, "k", now)
	require.NoError(t, err)
	require.Contains(t, rel, "Inbox/")
}

// A title that sanitizes to empty (all forbidden chars) is quarantined.
func TestWriteQuarantinesEmptyRenderedFilename(t *testing.T) {
	prof := clipProfile(t)
	root := t.TempDir()
	q := filepath.Join(t.TempDir(), "failed")
	_, err := Write(root, prof, "clip", map[string]any{"title": "///"}, "b", q, "k", now)
	require.ErrorIs(t, err, ErrQuarantined)
}
