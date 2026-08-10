package ingest

import (
	"bytes"
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

// capWriter is the stdout bound: it must never let more than cap bytes reach
// the buffer, yet always claim the full write so the hook's pipe never blocks
// (SPEC §31.7 — a hook that floods stdout cannot wedge or OOM the ingest).
func TestCapWriterBoundsBufferButNeverBlocks(t *testing.T) {
	var buf bytes.Buffer
	c := &capWriter{w: &buf, cap: 4}

	n, err := c.Write([]byte("hello")) // 5 bytes into a 4-byte cap
	require.NoError(t, err)
	require.Equal(t, 5, n, "claims the full write even when it drops the overflow")
	require.Equal(t, "hell", buf.String(), "buffer stops exactly at the cap")

	n, err = c.Write([]byte("more")) // already full: everything dropped
	require.NoError(t, err)
	require.Equal(t, 4, n, "still claims the full write so the hook keeps draining")
	require.Equal(t, "hell", buf.String(), "nothing past the cap ever lands")
	require.LessOrEqual(t, buf.Len(), 4)
}

// Both hooks configured: ## Metadata (probe, fenced) must precede ##
// Transcript (transcribe, markup-neutralized) — the §31.7 section order, and
// the transcript path escapes wikilink openers exactly like the probe-only
// path does not need to (probe is fenced, transcript is inline).
func TestMediaBodyRunsBothHooksInOrder(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	transcribe := filepath.Join(dir, "transcribe")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\nprintf 'codec=aac\\n'\n"), 0o755))
	require.NoError(t, os.WriteFile(transcribe,
		[]byte("#!/bin/sh\nprintf 'see [[secret]] and ![[img.png]]\\n'\n"), 0o755))

	h := MediaHooks{
		ProbeCmd:      []string{probe},
		TranscribeCmd: []string{transcribe},
		Timeout:       30 * time.Second,
	}
	body := mediaBody(context.Background(), h, filepath.Join(dir, "x.mp4"))

	meta := strings.Index(body, "## Metadata")
	tr := strings.Index(body, "## Transcript")
	require.GreaterOrEqual(t, meta, 0, "metadata section present")
	require.GreaterOrEqual(t, tr, 0, "transcript section present")
	require.Less(t, meta, tr, "metadata must render before transcript (SPEC §31.7)")
	require.Contains(t, body, "codec=aac")
	require.NotContains(t, body, "![[", "transcript must not mint an embed")
	require.Equal(t, -1, firstLiveWikilinkOpener(body[tr:]),
		"transcript must not mint a live wikilink: %q", body[tr:])
	require.Contains(t, body, "secret", "transcript text survives neutralization")
}

// A hook that floods stdout past the 10 MiB cap is truncated, and the note
// says so — the §31.7/§21 body-cap invariant, exercised end-to-end (not just
// on capWriter): the returned body carries the truncation marker.
func TestRunMediaHookTruncationMarker(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	flood := filepath.Join(dir, "flood")
	// 11 MiB of printable bytes (> the 10 MiB cap). tr converts NULs so the
	// output is not stripped away as control bytes before the marker check.
	require.NoError(t, os.WriteFile(flood,
		[]byte("#!/bin/sh\nhead -c 11534336 /dev/zero | tr '\\0' a\n"), 0o755))

	h := MediaHooks{ProbeCmd: []string{flood}, Timeout: 30 * time.Second}
	body := mediaBody(context.Background(), h, filepath.Join(dir, "x.mp3"))
	require.Contains(t, body, "truncated at 10 MiB", "over-cap output is marked truncated")
}

// The remote (URL) media branch of URLRecord, uncovered by the local-file
// e2e: a downloaded webm routes to KindMedia, lands as an asset note, and the
// configured probe hook runs against the spooled bytes.
func TestURLRecordMediaRunsHookOnSpool(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\nprintf 'codec=fake\\n'\n"), 0o755))

	// EBML magic → sniffs as video/webm without the extension map.
	g := fakeDownloader{t: t, body: []byte{0x1A, 0x45, 0xDF, 0xA3, 0x01, 0, 0, 0, 'x'}}
	h := MediaHooks{ProbeCmd: []string{probe}, Timeout: 30 * time.Second}

	rec, cleanup, err := URLRecord(context.Background(), g, "https://example.com/clip.webm", testTypes, h, 0, testNow)
	defer cleanup()
	require.NoError(t, err)
	require.Equal(t, "asset", rec.NoteType)
	require.Equal(t, "video/webm", rec.Fields["mime"])
	require.Contains(t, rec.Body, "## Metadata")
	require.Contains(t, rec.Body, "codec=fake", "the hook ran against the spooled download")
}
