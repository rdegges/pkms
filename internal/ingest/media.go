package ingest

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// mediaExtMap reclassifies a LOCAL file that sniffs as
// application/octet-stream when its extension names a container
// http.DetectContentType misses (SPEC §31.1). Every entry is proven by a
// table test against the sniffer (media_test.go) — the sniffer already
// routes avi/webm/ogg/wav and ID3-tagged mp3 to a media type, so those are
// deliberately absent. Remote URLs never consult this map (§31.1).
var mediaExtMap = map[string]string{
	".mp3":  "audio/mpeg", // frame-sync start (no ID3) sniffs as octet-stream
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
}

// mediaMIMEForLocalExt returns the reclassified media MIME for a local
// octet-stream file, or "" if the extension is not a known missniff.
func mediaMIMEForLocalExt(path string) string {
	return mediaExtMap[strings.ToLower(filepath.Ext(path))]
}

// isMediaMIME reports the §31.1 media types (audio/*, video/*,
// application/ogg) that route to the media handler.
func isMediaMIME(mime string) bool {
	return strings.HasPrefix(mime, "audio/") ||
		strings.HasPrefix(mime, "video/") ||
		strings.HasPrefix(mime, "application/ogg")
}

// MediaHooks are the resolved §31.7 hooks for one push run.
type MediaHooks struct {
	TranscribeCmd []string
	ProbeCmd      []string
	Timeout       time.Duration
}

// mediaHookStdoutCap bounds each hook's stdout (SPEC §31.7 — §21 body-cap
// consistency).
const mediaHookStdoutCap = 10 << 20

// mediaBody builds a media asset note's body (SPEC §31.7). It runs the
// configured hooks against localPath (push mode only — the caller passes
// zero hooks in pull mode), neutralizes their hostile stdout, and always
// returns a body: a hint when nothing is configured, so a media note never
// depends on a hook to land.
func mediaBody(ctx context.Context, h MediaHooks, localPath string) string {
	var b strings.Builder
	ran := false

	if len(h.ProbeCmd) > 0 {
		ran = true
		out, err := runMediaHook(ctx, h.ProbeCmd, localPath, h.Timeout)
		b.WriteString("## Metadata\n\n")
		if err != nil {
			fmt.Fprintf(&b, "> `%s` failed: %s\n", h.ProbeCmd[0], oneLine(err.Error()))
		} else {
			b.WriteString(fenceControlled(out))
		}
		b.WriteString("\n")
	}

	if len(h.TranscribeCmd) > 0 {
		ran = true
		out, err := runMediaHook(ctx, h.TranscribeCmd, localPath, h.Timeout)
		b.WriteString("## Transcript\n\n")
		if err != nil {
			fmt.Fprintf(&b, "> `%s` failed: %s\n", h.TranscribeCmd[0], oneLine(err.Error()))
		} else {
			b.WriteString(neutralizeMarkup(out))
			b.WriteString("\n")
		}
	}

	if !ran {
		return "> No media metadata: set `probe_cmd` and/or `transcribe_cmd` " +
			"under `[vaults.assets]` to extract it (e.g. ffprobe, whisper).\n"
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// runMediaHook execs argv with localPath appended as the final argument
// (SPEC §31.7): no shell ever, stdout capped, stderr discarded, bounded by
// timeout. Returns the captured stdout (possibly truncated) or an error.
func runMediaHook(ctx context.Context, argv []string, localPath string, timeout time.Duration) (string, error) {
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	full := append(append([]string{}, argv...), localPath)
	cmd := exec.CommandContext(hctx, full[0], full[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &capWriter{w: &buf, cap: mediaHookStdoutCap}
	cmd.Stderr = nil
	// A hook that forks (e.g. a shell running a real tool) can leave a
	// grandchild holding the stdout pipe after the deadline kill, blocking
	// Wait until it exits. WaitDelay force-closes the pipes shortly after
	// the kill so a wedged hook can never hang the ingest past the timeout.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if hctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("timed out after %s", timeout)
	}
	out := buf.String()
	if len(out) >= mediaHookStdoutCap {
		out += "\n[output truncated at 10 MiB]"
	}
	if err != nil {
		return "", err
	}
	return out, nil
}

// capWriter bounds how many bytes reach the underlying buffer; writes past
// the cap are dropped (the hook keeps running — the pipe never blocks).
type capWriter struct {
	w   *bytes.Buffer
	cap int
}

func (c *capWriter) Write(p []byte) (int, error) {
	if room := c.cap - c.w.Len(); room > 0 {
		if len(p) > room {
			c.w.Write(p[:room])
		} else {
			c.w.Write(p)
		}
	}
	return len(p), nil // always claim the full write so the hook never blocks
}

// fenceControlled wraps probe output in a code fence one backtick longer
// than the longest backtick run inside it (SPEC §31.7), so output that
// itself contains fences cannot break out. Control bytes are stripped so a
// terminal escape can never survive into the note.
func fenceControlled(s string) string {
	s = stripControl(s)
	longest := 0
	run := 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longest+1)
	if len(fence) < 3 {
		fence = "```"
	}
	return fence + "\n" + strings.TrimRight(s, "\n") + "\n" + fence + "\n"
}

// neutralizeMarkup strips control bytes and escapes every `[` so
// transcript text mints neither embeds (`![[`) nor graph edges (`[[`) —
// the §28.9 posture, identical to the PDF body path (§31.6).
func neutralizeMarkup(s string) string {
	return strings.ReplaceAll(stripControl(s), "[", `\[`)
}

// stripControl removes C0/DEL control bytes except \n and \t; a bare \r
// becomes a space so a CR line break never glues words (matching the PDF
// path).
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, s)
}

// oneLine collapses whitespace runs to single spaces for a one-line hint.
func oneLine(s string) string {
	return strings.Join(strings.Fields(stripControl(s)), " ")
}
