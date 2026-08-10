// Package ingest freezes the phase-2 ingester contract (SPEC §7): typed
// records, per-record acknowledgement, and the telegraf-style registry.
// No ingester implementations ship in v1 — the interfaces and the state
// store are load-bearing for phase 2 and frozen now.
package ingest

import (
	"context"
	"fmt"
	"io"
	"sort"
)

// Record is one typed unit from a source. Ingesters emit records,
// never markdown strings.
type Record struct {
	// NaturalKey uniquely identifies the record at its source:
	// email Message-ID, canonical URL, or file SHA-256.
	NaturalKey string
	// NoteType names the profile note type whose schema validates Fields.
	NoteType string
	// Fields become frontmatter after schema validation.
	Fields map[string]any
	// Body is the markdown body (already converted upstream).
	Body string
	// Assets are stored per the asset-size policy (phase 2.5).
	Assets []Asset
}

// Asset is a binary attachment carried by a record.
type Asset struct {
	Filename string
	SHA256   string
	Size     int64
	Open     func() (io.ReadCloser, error)
	// LocalPath, when non-empty, is the absolute path of a file the user
	// already owns on this machine; the storage policy references an
	// over-threshold local asset in place instead of copying it (SPEC
	// §31.2). Additive to the frozen §7 shape.
	LocalPath string
}

// Cursor is source-private resume state (e.g. IMAP UIDVALIDITY+UIDNEXT),
// persisted through the state store's cursor lines.
type Cursor map[string]any

// EmitFunc delivers one record to the pipeline. It returns ONLY after the
// record is durable: note written atomically AND the ack appended (fsync'd)
// to the source state file — or after the record was quarantined or
// deduplicated. A non-nil error means the pipeline is failing and the
// ingester must stop.
type EmitFunc func(ctx context.Context, r Record) error

// Ingester streams records from one source.
type Ingester interface {
	// Name is the registry key ("imap", "rss").
	Name() string
	// Fetch streams records since cursor. Implementations MUST tolerate
	// re-emitting already-acked records: crash recovery is re-fetch +
	// dedup, so the pipeline drops anything whose key is already acked.
	Fetch(ctx context.Context, cursor Cursor, emit EmitFunc) error
}

// Factory builds an ingester from its [[vaults.ingesters]] config table.
type Factory func(cfg map[string]any) (Ingester, error)

var registry = map[string]Factory{}

// Register adds an ingester factory (called from init() in its package).
func Register(name string, f Factory) {
	if _, dup := registry[name]; dup {
		panic("duplicate ingester " + name)
	}
	registry[name] = f
}

// Lookup returns the factory for name.
func Lookup(name string) (Factory, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown ingester %q (registered: %v)", name, Registered())
	}
	return f, nil
}

// Registered lists registered ingester names, sorted.
func Registered() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
