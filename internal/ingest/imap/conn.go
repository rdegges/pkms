package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// dialConnectTimeout bounds the TCP+TLS handshake when the caller's context
// has no earlier deadline.
const dialConnectTimeout = 20 * time.Second

// dialTLS opens the real connection: implicit TLS only (SPEC §23 — no
// STARTTLS, no plaintext in v1), then authenticates per config.
func dialTLS(ctx context.Context, cfg Config) (Conn, error) {
	// Resolve credentials BEFORE dialing: a missing secret must fail fast
	// with its how-to-fix copy, not after a network round-trip.
	m := &IMAP{cfg: cfg}
	var pass, token string
	var err error
	switch cfg.Auth {
	case "password":
		if pass, err = m.password(); err != nil {
			return nil, err
		}
	case "xoauth2":
		if token, err = m.accessToken(ctx); err != nil {
			return nil, err
		}
	}

	// Dial and hand-shake under the caller's deadline (SPEC §17 wall-clock
	// bound): a silent/half-open server can never hang cron. imapclient's
	// own DialTLS takes no context, so dial the TLS conn ourselves.
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	dialCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, dialConnectTimeout)
		defer cancel()
	}
	tlsDialer := &tls.Dialer{NetDialer: &net.Dialer{}, Config: &tls.Config{ServerName: cfg.Host}}
	conn, err := tlsDialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	// Bound every later read/write by the run deadline so Examine/Fetch
	// can't block forever on a wedged connection.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c := imapclient.New(conn, nil)
	switch cfg.Auth {
	case "password":
		if err := c.Login(cfg.Username, pass).Wait(); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("login %s as %s: %w (update the password with `pkms secret set %s password`, or check the account's app-password settings)", cfg.Host, cfg.Username, err, cfg.SourceName)
		}
	case "xoauth2":
		if err := c.Authenticate(newXOAUTH2Client(cfg.Username, token)); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("XOAUTH2 auth %s as %s: %w (token may be revoked; re-run `pkms auth %s`)", cfg.Host, cfg.Username, err, cfg.SourceName)
		}
	}
	return &realConn{c: c}, nil
}

type realConn struct {
	c *imapclient.Client
}

func (r *realConn) Examine(mailbox string) (uint32, uint32, error) {
	// ReadOnly = the EXAMINE command: pkms never opens a mailbox writable.
	data, err := r.c.Select(mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return 0, 0, err
	}
	return data.UIDValidity, uint32(data.UIDNext), nil
}

func (r *realConn) FetchSince(lowUID uint32, max int, fn func(uid uint32, raw []byte, internalDate time.Time) error) error {
	var set imap.UIDSet
	set.AddRange(imap.UID(lowUID), 0) // 0 = '*'

	bodySection := &imap.FetchItemBodySection{Peek: true} // never sets \Seen
	cmd := r.c.Fetch(set, &imap.FetchOptions{
		UID:          true,
		InternalDate: true,
		BodySection:  []*imap.FetchItemBodySection{bodySection},
	})
	defer func() { _ = cmd.Close() }()

	n := 0
	for {
		if n >= max {
			// Batch cap (SPEC §23): stop cleanly; the cursor advances to
			// the highest processed UID and cron picks up the rest.
			return nil
		}
		msg := cmd.Next()
		if msg == nil {
			return cmd.Close()
		}
		uid, internalDate, raw, oversized, err := readMessage(msg, bodySection)
		if err != nil {
			return err
		}
		// Always report the UID — even for an oversized (streamed under the
		// cap, not buffered) or body-less message — with raw==nil signaling
		// "skip". The caller advances its cursor past skipped UIDs so a
		// too-big newest message can't tarpit every future run (SPEC §28.6).
		if oversized {
			raw = nil
		}
		if err := fn(uid, raw, internalDate); err != nil {
			return err
		}
		n++
	}
}

// maxMessageBytes bounds one whole RFC822 message read into memory (SPEC
// §21): a normal mail with attachments stays well under this; a hostile
// multi-hundred-MiB message is streamed to the cap and skipped.
const maxMessageBytes = 25 << 20

// readMessage streams one message's items, reading the body section under
// maxMessageBytes rather than buffering the whole thing with Collect().
func readMessage(msg *imapclient.FetchMessageData, bodySection *imap.FetchItemBodySection) (uid uint32, internalDate time.Time, raw []byte, oversized bool, err error) {
	for {
		item := msg.Next()
		if item == nil {
			return uid, internalDate, raw, oversized, nil
		}
		switch it := item.(type) {
		case imapclient.FetchItemDataUID:
			uid = uint32(it.UID)
		case imapclient.FetchItemDataInternalDate:
			internalDate = it.Time
		case imapclient.FetchItemDataBodySection:
			// Read cap+1 to detect overflow; drain fully either way so
			// the next item parses.
			b, rerr := io.ReadAll(io.LimitReader(it.Literal, maxMessageBytes+1))
			if rerr != nil {
				return uid, internalDate, nil, false, rerr
			}
			if int64(len(b)) > maxMessageBytes {
				oversized = true
				b = nil
			}
			raw = b
		}
	}
}

func (r *realConn) Close() error {
	return r.c.Close()
}
