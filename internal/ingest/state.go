package ingest

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StateStore is the per-source dedup/ack ledger (SPEC §7): an append-only
// NDJSON file under $XDG_STATE_HOME/pkms/state/<vault>/<source>.ndjson.
// An ack line is appended and fsync'd only AFTER the note rename succeeds;
// a crash between write and ack re-fetches the record and dedup makes the
// retry a no-op — never duplicates, never loses.
type StateStore struct {
	path   string
	source string

	f      *os.File
	seen   map[string]bool // sha256(NaturalKey) hex -> acked or quarantined
	cursor Cursor
	lines  int
}

type stateLine struct {
	// Header fields (first line).
	V            int    `json:"v,omitempty"`
	Source       string `json:"source,omitempty"`
	CursorSchema string `json:"cursor_schema,omitempty"`

	// Op lines.
	Op     string `json:"op,omitempty"` // ack | quarantine | cursor
	K      string `json:"k,omitempty"`  // sha256(NaturalKey), hex
	Note   string `json:"note,omitempty"`
	Reason string `json:"reason,omitempty"`
	Data   Cursor `json:"data,omitempty"`
	TS     string `json:"ts,omitempty"`
}

// compactThreshold triggers a rewrite on open (SPEC §7).
const compactThreshold = 10000

// Key hashes a natural key into its state-file form.
func Key(naturalKey string) string {
	sum := sha256.Sum256([]byte(naturalKey))
	return hex.EncodeToString(sum[:])
}

// OpenState opens (creating if needed) the state file for one source.
func OpenState(path, source string) (*StateStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	s := &StateStore{path: path, source: source, seen: map[string]bool{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	if s.lines > compactThreshold {
		if err := s.compact(); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.f = f
	if s.lines == 0 {
		if err := s.append(stateLine{V: 1, Source: source}); err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *StateStore) load() error {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		s.lines++
		var l stateLine
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			return fmt.Errorf("%s line %d: %w", s.path, s.lines, err)
		}
		switch l.Op {
		case "ack", "quarantine":
			s.seen[l.K] = true
		case "cursor":
			s.cursor = l.Data
		}
	}
	return sc.Err()
}

// Seen reports whether a natural key was already acked or quarantined.
func (s *StateStore) Seen(naturalKey string) bool {
	return s.seen[Key(naturalKey)]
}

// Ack records a durably-written note for the key. Fsync'd before returning.
func (s *StateStore) Ack(naturalKey, notePath string, now time.Time) error {
	k := Key(naturalKey)
	if err := s.append(stateLine{Op: "ack", K: k, Note: notePath, TS: now.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	s.seen[k] = true
	return nil
}

// Quarantine records a rejected record so re-fetches skip it too.
func (s *StateStore) Quarantine(naturalKey, reason string, now time.Time) error {
	k := Key(naturalKey)
	if err := s.append(stateLine{Op: "quarantine", K: k, Reason: reason, TS: now.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	s.seen[k] = true
	return nil
}

// SetCursor persists source resume state.
func (s *StateStore) SetCursor(c Cursor, now time.Time) error {
	if err := s.append(stateLine{Op: "cursor", Data: c, TS: now.UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	s.cursor = c
	return nil
}

// Cursor returns the last persisted cursor (nil if none).
func (s *StateStore) Cursor() Cursor { return s.cursor }

func (s *StateStore) append(l stateLine) error {
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := s.f.Sync(); err != nil {
		return err
	}
	s.lines++
	return nil
}

// compact rewrites the log as header + one ack per seen key + cursor,
// atomically replacing the old file.
func (s *StateStore) compact() error {
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".pkms-state-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	if err := enc.Encode(stateLine{V: 1, Source: s.source}); err != nil {
		return err
	}
	lines := 1
	for k := range s.seen {
		if err := enc.Encode(stateLine{Op: "ack", K: k}); err != nil {
			return err
		}
		lines++
	}
	if s.cursor != nil {
		if err := enc.Encode(stateLine{Op: "cursor", Data: s.cursor}); err != nil {
			return err
		}
		lines++
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return err
	}
	s.lines = lines
	return nil
}

// Close releases the file handle.
func (s *StateStore) Close() error { return s.f.Close() }
