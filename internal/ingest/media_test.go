package ingest

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Every mediaExtMap entry must be JUSTIFIED: http.DetectContentType must
// actually miss it to application/octet-stream (SPEC §31.1 — entries are
// admitted on test evidence, never assumption). A header the sniffer
// already classifies has no business in the map.
func TestMediaExtMapEntriesAreProvenMissniffs(t *testing.T) {
	// Minimal magic bytes for each mapped container, as a real file starts.
	headers := map[string][]byte{
		".mp3":  {0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00}, // MPEG frame sync, no ID3
		".m4a":  append([]byte{0, 0, 0, 0x18}, []byte("ftypM4A ")...),
		".flac": []byte("fLaC\x00\x00\x00\x22"),
		".mp4":  append([]byte{0, 0, 0, 0x18}, []byte("ftypisom")...),
		".m4v":  append([]byte{0, 0, 0, 0x18}, []byte("ftypM4V ")...),
		".mov":  append([]byte{0, 0, 0, 0x14}, []byte("ftypqt  ")...),
	}
	for ext := range mediaExtMap {
		t.Run(ext, func(t *testing.T) {
			h, ok := headers[ext]
			require.True(t, ok, "map entry %s has no missniff fixture — add one or drop the entry", ext)
			got := http.DetectContentType(h)
			require.Equal(t, "application/octet-stream", got,
				"%s sniffs as %q, so it does not need the extension map (SPEC §31.1)", ext, got)
		})
	}
}

// Containers the sniffer ALREADY handles must NOT be in the map (they
// route to KindMedia via the MIME, so mapping them would be dead weight —
// pinned so a future edit can't quietly add a redundant entry).
func TestMediaExtMapExcludesSniffableContainers(t *testing.T) {
	for _, ext := range []string{".avi", ".webm", ".mkv", ".ogg", ".wav"} {
		_, present := mediaExtMap[ext]
		require.False(t, present, "%s is sniffable; it must not be in the extension map", ext)
	}
}

func TestIsMediaMIME(t *testing.T) {
	for _, m := range []string{"audio/mpeg", "video/mp4", "video/webm", "application/ogg"} {
		require.True(t, isMediaMIME(m), m)
	}
	for _, m := range []string{"text/html", "application/pdf", "application/octet-stream", "image/png"} {
		require.False(t, isMediaMIME(m), m)
	}
}

func TestMediaBodyUnconfiguredHint(t *testing.T) {
	body := mediaBody(context.Background(), MediaHooks{}, "/tmp/x.mp3")
	require.Contains(t, body, "probe_cmd")
	require.Contains(t, body, "transcribe_cmd")
	require.NotContains(t, body, "## Metadata")
	require.NotContains(t, body, "## Transcript")
}

func TestFenceControlledEscapesInnerFences(t *testing.T) {
	out := fenceControlled("```` already fenced ````")
	lines := strings.SplitN(out, "\n", 2)
	require.GreaterOrEqual(t, len(lines[0]), 5, "outer fence must be longer than the 4-backtick run inside")
	require.True(t, strings.HasPrefix(lines[0], "`````"), "got %q", lines[0])
}

func TestFenceControlledStripsControlBytes(t *testing.T) {
	out := fenceControlled("codec=aac\x1b]0;pwn\x07\n")
	require.NotContains(t, out, "\x1b")
	require.NotContains(t, out, "\x07")
	require.Contains(t, out, "codec=aac")
}

func TestNeutralizeMarkupKillsEmbedsAndEdges(t *testing.T) {
	got := neutralizeMarkup("see ![[secret.png]] and [[Note]] and [12]")
	require.NotContains(t, got, "![[")
	require.Equal(t, -1, firstLiveWikilinkOpener(got), "no live wikilink in %q", got)
	require.Contains(t, got, "secret.png", "text survives")
}

func TestStripControlKeepsTabsNewlinesSpacesCR(t *testing.T) {
	require.Equal(t, "a\tb\nc d", stripControl("a\tb\nc\rd"))
	require.NotContains(t, stripControl("x\x00y\x1bz"), "\x00")
}

// The hook executor end-to-end, via a scripted hook: mediaBody execs it
// (argv[0] is the script — no shell), captures stdout, and fences it.
func TestRunMediaHookCapsAndNeutralizes(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "probe")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf 'codec=aac\\n```fence```\\n'\n"), 0o755))
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	h := MediaHooks{ProbeCmd: []string{script}, Timeout: 30 * time.Second}
	body := mediaBody(context.Background(), h, filepath.Join(dir, "x.mp3"))
	require.Contains(t, body, "## Metadata")
	require.Contains(t, body, "codec=aac")
	require.Contains(t, body, "````", "inner ``` fence forces a longer outer fence")
}

func TestRunMediaHookTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "slow")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755))
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	h := MediaHooks{ProbeCmd: []string{script}, Timeout: 500 * time.Millisecond}
	start := time.Now()
	body := mediaBody(context.Background(), h, filepath.Join(dir, "x.mp3"))
	require.Less(t, time.Since(start), 10*time.Second)
	require.Contains(t, body, "timed out")
}

func TestRunMediaHookFailureStillLandsNote(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "boom")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho oops >&2\nexit 3\n"), 0o755))
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	h := MediaHooks{TranscribeCmd: []string{script}, Timeout: 30 * time.Second}
	body := mediaBody(context.Background(), h, filepath.Join(dir, "x.mp3"))
	require.Contains(t, body, "## Transcript")
	require.Contains(t, body, "failed")
}

func TestFileRecordMediaByExtension(t *testing.T) {
	// A local .mp3 whose bytes sniff as octet-stream is reclassified to
	// KindMedia (SPEC §31.1) and gets a media body.
	dir := t.TempDir()
	p := filepath.Join(dir, "song.mp3")
	require.NoError(t, os.WriteFile(p, []byte{0xFF, 0xFB, 0x90, 0x00, 1, 2, 3, 4}, 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, MediaHooks{}, testNow)
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "audio/mpeg", rec.Fields["mime"], "extension reclassified the octet-stream sniff")
	require.Contains(t, rec.Body, "probe_cmd", "unconfigured media note carries the hint")
	require.Len(t, rec.Assets, 1)
}

func TestFileRecordMediaBySniff(t *testing.T) {
	// A .bin file that sniffs as a real media type (webm) routes to media
	// WITHOUT the extension map.
	dir := t.TempDir()
	p := filepath.Join(dir, "clip.bin")
	require.NoError(t, os.WriteFile(p, []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0, 0, 0}, 0o644))

	rec, err := FileRecord(context.Background(), p, testTypes, MediaHooks{}, testNow)
	require.NoError(t, err)
	require.Equal(t, "video/webm", rec.Fields["mime"])
	require.Contains(t, rec.Body, "No media metadata")
}
