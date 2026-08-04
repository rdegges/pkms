// Package imap ingests email over IMAP (SPEC §23). Strictly read-only:
// mailboxes are EXAMINEd, bodies fetched with Peek, and pkms never sets
// flags, deletes, or expunges. Hostile email is the normal case (SPEC §14).
package imap

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/ingest"
	"github.com/rdegges/pkms/internal/secrets"
)

const defaultBatch = 200

func init() {
	ingest.Register("imap", func(cfg map[string]any) (ingest.Ingester, error) { return New(cfg) })
}

// Config is one [[vaults.ingesters]] imap table (SPEC §18), strict-keyed.
type Config struct {
	Host        string
	Port        int
	Username    string
	Auth        string // "password" | "xoauth2"
	Mailbox     string
	Batch       int
	NoteType    string
	PasswordCmd []string
	TokenURL    string // xoauth2 refresh endpoint; defaults to Google's
	AuthURL     string // xoauth2 authorization endpoint; defaults to Google's

	// Vault + source identity, injected by the pipeline for secret lookup.
	VaultName  string
	SourceName string
}

// IMAP is the ingester. Dial is swappable for tests; the default dials a
// real TLS connection (conn.go).
type IMAP struct {
	cfg   Config
	dial  func(ctx context.Context, cfg Config) (Conn, error)
	reset bool
}

// Conn is the thin slice of the IMAP protocol the ingester needs; the
// real implementation wraps go-imap's imapclient (conn.go), tests fake it.
type Conn interface {
	// Examine opens the mailbox read-only.
	Examine(mailbox string) (uidValidity, uidNext uint32, err error)
	// FetchSince streams raw RFC822 messages with UID >= lowUID, at most
	// max, in UID order, with each message's INTERNALDATE.
	FetchSince(lowUID uint32, max int, fn func(uid uint32, raw []byte, internalDate time.Time) error) error
	Close() error
}

// New builds the ingester from its config table.
func New(cfg map[string]any) (*IMAP, error) {
	c := Config{Auth: "password", Mailbox: "INBOX", Port: 993, Batch: defaultBatch,
		TokenURL: "https://oauth2.googleapis.com/token", AuthURL: defaultAuthURL}
	for k, v := range cfg {
		var err error
		switch k {
		case "host":
			err = strKey(&c.Host, k, v)
		case "username":
			err = strKey(&c.Username, k, v)
		case "auth":
			err = strKey(&c.Auth, k, v)
		case "mailbox":
			err = strKey(&c.Mailbox, k, v)
		case "note_type":
			err = strKey(&c.NoteType, k, v)
		case "token_url":
			err = strKey(&c.TokenURL, k, v)
		case "auth_url":
			err = strKey(&c.AuthURL, k, v)
		case "port":
			err = intKey(&c.Port, k, v)
		case "batch":
			err = intKey(&c.Batch, k, v)
		case "password_cmd":
			c.PasswordCmd, err = argvKey(k, v)
		default:
			err = fmt.Errorf("unknown config key %q (known: host, port, username, auth, mailbox, batch, note_type, auth_url, token_url, password_cmd)", k)
		}
		if err != nil {
			return nil, fmt.Errorf("imap: %w", err)
		}
	}
	if c.Host == "" {
		return nil, fmt.Errorf(`imap: host is required, e.g. host = "imap.fastmail.com"`)
	}
	if c.Username == "" {
		return nil, fmt.Errorf(`imap: username is required`)
	}
	if c.Auth != "password" && c.Auth != "xoauth2" {
		return nil, fmt.Errorf(`imap: auth must be "password" or "xoauth2", got %q`, c.Auth)
	}
	if c.Batch < 1 {
		return nil, fmt.Errorf("imap: batch must be >= 1")
	}
	return &IMAP{cfg: c, dial: dialTLS}, nil
}

func (m *IMAP) Name() string { return "imap" }

// CursorSchema is the on-disk cursor format tag (SPEC §7/§23).
func (m *IMAP) CursorSchema() string { return "imap/1" }

// SetNoteType is the pipeline's profile-default hook.
func (m *IMAP) SetNoteType(t string) {
	if m.cfg.NoteType == "" {
		m.cfg.NoteType = t
	}
}

// SetIdentity injects vault/source identity for secret resolution.
func (m *IMAP) SetIdentity(vaultName, sourceName string) {
	m.cfg.VaultName = vaultName
	m.cfg.SourceName = sourceName
}

// CursorWasReset reports a UIDVALIDITY change (pipeline Result surface).
func (m *IMAP) CursorWasReset() bool { return m.reset }

// Fetch implements the §7 contract with the §23 resume algorithm.
func (m *IMAP) Fetch(ctx context.Context, cursor ingest.Cursor, emit ingest.EmitFunc) error {
	conn, err := m.dial(ctx, m.cfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	uidValidity, uidNext, err := conn.Examine(m.cfg.Mailbox)
	if err != nil {
		return fmt.Errorf("examine %q: %w", m.cfg.Mailbox, err)
	}

	lastUID := cursorUint(cursor, "last_uid")
	if v := cursorUint(cursor, "uidvalidity"); v != 0 && v != uint64(uidValidity) {
		// The server renumbered the mailbox: stored UIDs are meaningless.
		// Full pass; dedup makes it a cheap no-op sweep (SPEC §23).
		m.reset = true
		lastUID = 0
	}

	low := lastUID + 1
	maxSeen := lastUID
	// low >= UIDNEXT means nothing new — and skipping the fetch entirely
	// sidesteps the `X:*` gotcha, where X above every existing UID makes
	// servers return the LAST message instead of an empty set (SPEC §23).
	if uidNext == 0 || low < uint64(uidNext) {
		err = conn.FetchSince(uint32(low), m.cfg.Batch, func(uid uint32, raw []byte, internalDate time.Time) error {
			// Advance the cursor past every UID the server handed us, even
			// a skipped one (nil raw = oversized or body-less), so it can
			// never be re-fetched forever (SPEC §28.6).
			if uint64(uid) > maxSeen {
				maxSeen = uint64(uid)
			}
			if raw == nil {
				return nil
			}
			rec, rerr := m.record(raw, internalDate)
			if rerr != nil {
				return fmt.Errorf("message uid %d: %w", uid, rerr)
			}
			return emit(ctx, rec)
		})
		if err != nil {
			return err
		}
	}

	cursor["uidvalidity"] = uint64(uidValidity)
	cursor["last_uid"] = maxSeen
	return nil
}

// cursorUint reads a cursor number that may have round-tripped through
// JSON (float64) or not (uint64/int).
func cursorUint(c ingest.Cursor, key string) uint64 {
	switch v := c[key].(type) {
	case uint64:
		return v
	case int:
		return uint64(v)
	case int64:
		return uint64(v)
	case float64:
		return uint64(v)
	default:
		return 0
	}
}

// strKey/intKey/argvKey: strict config decoding with actionable errors.
func strKey(dst *string, k string, v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", k)
	}
	*dst = s
	return nil
}

func intKey(dst *int, k string, v any) error {
	switch n := v.(type) {
	case int64:
		*dst = int(n)
	case int:
		*dst = n
	default:
		return fmt.Errorf("%s must be an integer", k)
	}
	return nil
}

func argvKey(k string, v any) ([]string, error) {
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf(`%s must be an argv array like ["op", "read", "op://vault/item/password"] — never a shell string`, k)
	}
	argv := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings", k)
		}
		argv = append(argv, s)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("%s must not be empty", k)
	}
	return argv, nil
}

// password resolves the account credential (SPEC §24).
func (m *IMAP) password() (string, error) {
	source := "imap:" + m.cfg.SourceName
	return secrets.Resolve(m.cfg.VaultName, source, m.cfg.SourceName, secrets.Password, m.cfg.PasswordCmd)
}

// accessToken refreshes an XOAUTH2 access token from the stored refresh
// token (SPEC §24: tokens are refreshed every run, never persisted).
func (m *IMAP) accessToken(ctx context.Context) (string, error) {
	source := "imap:" + m.cfg.SourceName
	get := func(kind secrets.Kind) (string, error) {
		return secrets.Resolve(m.cfg.VaultName, source, m.cfg.SourceName, kind, nil)
	}
	clientID, err := get(secrets.OAuthClientID)
	if err != nil {
		return "", fmt.Errorf("%w\n(xoauth2 needs oauth-client-id, oauth-client-secret and oauth-refresh-token; run `pkms auth %s` once to set them up)", err, m.cfg.SourceName)
	}
	clientSecret, err := get(secrets.OAuthClientSecret)
	if err != nil {
		return "", err
	}
	refresh, err := get(secrets.OAuthRefreshTok)
	if err != nil {
		return "", fmt.Errorf("%w\n(run `pkms auth %s` once to authorize and store the refresh token)", err, m.cfg.SourceName)
	}
	return refreshAccessToken(ctx, m.cfg.TokenURL, clientID, clientSecret, refresh, m.cfg.SourceName)
}

// xoauth2String builds the SASL initial response (SPEC §24).
func xoauth2String(username, accessToken string) string {
	return fmt.Sprintf("user=%s\x01auth=Bearer %s\x01\x01", username, accessToken)
}

// normalizeMessageID strips angle brackets and whitespace; comparison
// stays case-sensitive per RFC 5322 (SPEC §23).
func normalizeMessageID(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// usableMessageID rejects the malformed tail: empty, no @, absurd length.
func usableMessageID(id string) bool {
	return id != "" && strings.Contains(id, "@") && len(id) <= 998
}
