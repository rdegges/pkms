package imap

import (
	"context"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

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

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	c, err := imapclient.DialTLS(addr, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	switch cfg.Auth {
	case "password":
		if err := c.Login(cfg.Username, pass).Wait(); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("login %s as %s: %w (check the password with `pkms secret set`, or the account's app-password settings)", cfg.Host, cfg.Username, err)
		}
	case "xoauth2":
		if err := c.Authenticate(newXOAUTH2Client(cfg.Username, token)); err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("XOAUTH2 auth %s as %s: %w (token may be revoked; re-run `pkms auth`)", cfg.Host, cfg.Username, err)
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
		buf, err := msg.Collect()
		if err != nil {
			return err
		}
		raw := buf.FindBodySection(bodySection)
		if raw == nil {
			continue // server sent no body for this message; skip
		}
		if err := fn(uint32(buf.UID), raw, buf.InternalDate); err != nil {
			return err
		}
		n++
	}
}

func (r *realConn) Close() error {
	return r.c.Close()
}
