package imap

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"

	// Registers charset decoders so go-message transcodes non-UTF-8
	// parts and RFC 2047 headers itself.
	_ "github.com/emersion/go-message/charset"

	"github.com/rdegges/pkms/internal/fetch"
	"github.com/rdegges/pkms/internal/ingest"
)

const (
	// maxPartBytes caps one MIME part before conversion (SPEC §21).
	maxPartBytes = fetch.MaxHTMLBody
	// fallbackBodyPrefix feeds the no-Message-ID hash key (SPEC §23).
	fallbackBodyPrefix = 2 << 10
	// maxParts bounds a hostile many-part message (SPEC §23), enforced as a
	// flat part-count ceiling on what walkParts processes.
	maxParts = 100
	// maxMultipartHeaders bounds MIME nesting BEFORE go-message parses it.
	// Deeply-nested multipart is quadratic inside a single NextPart() call,
	// so a part-count loop cap can't stop it — this raw-byte scan can.
	maxMultipartHeaders = 100
)

type attachment struct {
	Filename string
	MIME     string
}

// record converts one raw RFC822 message into a pipeline record.
// internalDate backs the created field when the Date header is unusable.
func (m *IMAP) record(raw []byte, internalDate time.Time) (ingest.Record, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return ingest.Record{}, fmt.Errorf("parse message: %w", err)
	}
	h := mr.Header

	subject, _ := h.Subject() // RFC 2047 decoded; err tolerated (raw fallback inside)
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}

	rawID := h.Get("Message-Id")
	id := normalizeMessageID(rawID)
	key := id
	if !usableMessageID(id) {
		key = fallbackKey(h.Get("From"), h.Get("Date"), h.Get("Subject"), h.Get("To"), raw)
		id = "pkms-" + key[:32] + "@synthetic.invalid"
	}

	created := internalDate.UTC()
	if d, err := h.Date(); err == nil && !d.IsZero() {
		created = d.UTC()
	}

	fields := map[string]any{
		"title":   subject,
		"source":  "mid:" + id, // RFC 2392 (SPEC §23, question-round decision 4)
		"created": created.Format(time.RFC3339),
		"tags":    []any{"clip", "email"},
	}
	if from := addressList(h, "From"); len(from) > 0 {
		fields["from"] = from
	}
	if to := addressList(h, "To"); len(to) > 0 {
		fields["to"] = to
	}

	// Guard MIME nesting on the raw bytes BEFORE walking: a deeply-nested
	// multipart message is quadratic inside go-message's first NextPart,
	// so refuse to descend it at all. Header fields above are cheap and
	// already captured, so the note still lands (acked, never re-fetched)
	// with a body noting the skip — no wedge, no DoS (SPEC §23).
	if n := bytes.Count(bytes.ToLower(raw), []byte("multipart/")); n > maxMultipartHeaders {
		return ingest.Record{
			NaturalKey: key,
			NoteType:   m.cfg.NoteType,
			Fields:     fields,
			Body:       fmt.Sprintf("(message skipped: %d nested MIME parts exceed the safe limit; the headers above were still recorded)\n", n),
		}, nil
	}

	htmlPart, textPart, atts, err := walkParts(mr)
	if err != nil {
		return ingest.Record{}, err
	}

	var body string
	switch {
	case htmlPart != "":
		md, err := ingest.ConvertHTML(htmlPart)
		if err != nil {
			// Hostile HTML that breaks the converter still lands: fall
			// back to the text part or a stub, never lose the message.
			md = textPart
		}
		body = strings.TrimSpace(md)
	case textPart != "":
		body = strings.TrimSpace(textPart)
	}
	if body == "" {
		body = "(the message has no readable text or HTML body)"
	}
	if len(atts) > 0 {
		var b strings.Builder
		b.WriteString(body)
		b.WriteString("\n\n## Attachments\n\n")
		for _, a := range atts {
			name := a.Filename
			if name == "" {
				name = "(unnamed)"
			}
			fmt.Fprintf(&b, "- %s (%s) — not stored; attachment support lands in phase 2.5\n", name, a.MIME)
		}
		body = b.String()
	}

	return ingest.Record{
		NaturalKey: key,
		NoteType:   m.cfg.NoteType,
		Fields:     fields,
		Body:       body + "\n",
	}, nil
}

// walkParts collects the first text/html and text/plain inline parts and
// every attachment's name. go-message decodes charsets to UTF-8 as it
// reads; per-part bytes are capped (SPEC §21).
func walkParts(mr *mail.Reader) (htmlPart, textPart string, atts []attachment, err error) {
	parts := 0
	for {
		if parts >= maxParts {
			break // hostile part-count ceiling (SPEC §23)
		}
		parts++
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Unknown charsets/encodings are non-fatal in go-message;
			// anything else unreadable is skipped, not fatal — one bad
			// part must not lose the message.
			if p == nil {
				break
			}
			continue
		}
		switch ph := p.Header.(type) {
		case *mail.InlineHeader:
			ct, _, _ := ph.ContentType()
			switch {
			case ct == "text/html" && htmlPart == "":
				htmlPart = readCapped(p.Body)
			case ct == "text/plain" && textPart == "":
				textPart = readCapped(p.Body)
			}
		case *mail.AttachmentHeader:
			name, _ := ph.Filename()
			ct, _, _ := ph.ContentType()
			atts = append(atts, attachment{Filename: sanitizeAttachmentName(name), MIME: ct})
		}
	}
	return htmlPart, textPart, atts, nil
}

func readCapped(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, maxPartBytes))
	if err != nil {
		return string(b)
	}
	return string(b)
}

// sanitizeAttachmentName neutralizes traversal AND markdown/wikilink tricks
// in remote filenames before they land in note bodies (SPEC §21). A sender
// must not be able to smuggle `![[secret.png]]` (an Obsidian embed that
// renders arbitrary vault content) or `[[Note]]` (a fabricated graph edge)
// through an attachment filename.
func sanitizeAttachmentName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r < 0x20 || r == 0x7f:
			return -1
		case strings.ContainsRune("[]`|", r):
			return '-' // wikilink/embed/table/code markup
		default:
			return r
		}
	}, name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	name = strings.ReplaceAll(name, "..", "-")
	name = strings.TrimLeft(name, "!") // no leading embed marker
	if len(name) > 255 {
		name = name[:255]
	}
	return strings.TrimSpace(name)
}

func addressList(h mail.Header, field string) []any {
	addrs, err := h.AddressList(field)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	out := make([]any, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// fallbackKey hashes stable headers + a body prefix when Message-ID is
// missing or malformed (SPEC §23): Date+From+Subject breaks Message-ID
// reuse; the body prefix breaks templated-bulk collisions.
func fallbackKey(from, date, subject, to string, raw []byte) string {
	body := raw
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		body = raw[i+4:]
	} else if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		body = raw[i+2:]
	}
	if len(body) > fallbackBodyPrefix {
		body = body[:fallbackBodyPrefix]
	}
	hsh := sha256.New()
	for _, part := range []string{
		"pkms-mail", strings.ToLower(strings.TrimSpace(from)), date, subject, to,
	} {
		hsh.Write([]byte(part))
		hsh.Write([]byte{0})
	}
	hsh.Write(body)
	return hex.EncodeToString(hsh.Sum(nil))
}
