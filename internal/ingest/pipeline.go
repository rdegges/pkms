package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/assets"
	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/lock"
	"github.com/rdegges/pkms/internal/paths"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/snapshot"
	"github.com/rdegges/pkms/internal/vault"
	"github.com/rdegges/pkms/internal/writer"
)

// Result summarizes one source's run (SPEC §17 --json shape).
type Result struct {
	Source      string `json:"source"`
	New         int    `json:"new"`
	Deduped     int    `json:"deduped"`
	Quarantined int    `json:"quarantined"`
	CursorReset bool   `json:"cursor_reset,omitempty"`
	// Dropped counts records the ingester capped away this run — never a
	// silent cap (SPEC §21); they re-surface on later runs.
	Dropped int      `json:"dropped,omitempty"`
	Notes   []string `json:"notes"`
	// Existing lists the notes deduped records already live in, when known
	// (powers push mode's "already ingested → <path>" copy).
	Existing []string `json:"existing,omitempty"`

	// AlreadyRunning: another run holds the source lock (clean exit 0).
	AlreadyRunning bool `json:"-"`
}

// Summary is the human one-liner asserted by e2e (SPEC §17).
func (r *Result) Summary() string {
	if r.AlreadyRunning {
		return fmt.Sprintf("%s: already running", r.Source)
	}
	s := fmt.Sprintf("%s: %d new, %d deduped, %d quarantined", r.Source, r.New, r.Deduped, r.Quarantined)
	if r.Dropped > 0 {
		s += fmt.Sprintf(" (%d over the per-run cap, retried next run)", r.Dropped)
	}
	return s
}

// CursorResetter lets an ingester report that it discarded its resume
// state (e.g. IMAP UIDVALIDITY change). Optional — the frozen §7 contract
// has no slot for it, so the pipeline checks for this interface after Fetch.
type CursorResetter interface {
	CursorWasReset() bool
}

// noteTypeDefaulter receives the profile's [ingest] clip type when the
// ingester's config declared no note_type override (SPEC §18).
type noteTypeDefaulter interface {
	SetNoteType(string)
}

// dropReporter surfaces per-run caps so they are never silent (SPEC §21).
type dropReporter interface {
	DroppedItems() int
}

// cursorSchemaProvider lets a cursor-bearing ingester declare its cursor
// format (e.g. "imap/1") so the state header records it (SPEC §7).
type cursorSchemaProvider interface {
	CursorSchema() string
}

// identitySetter receives the vault/source identity ingesters need for
// keyring secret lookups (SPEC §24).
type identitySetter interface {
	SetIdentity(vaultName, sourceName string)
}

// Runner executes configured ingesters for one vault (SPEC §17).
type Runner struct {
	Vault   *config.Vault
	Profile *profile.Profile
	Now     func() time.Time

	// sourceIDs maps NaturalKey -> vault-relative note path for every
	// source_id already in the vault; updated as the run writes notes so
	// two sources emitting the same key within one run can't duplicate.
	sourceIDs map[string]string
}

// LoadSourceIDs scans the vault once for existing source_id frontmatter
// (SPEC §17 step 3 — closes the crash window between rename and ack).
func (r *Runner) LoadSourceIDs() error {
	ix, err := vault.BuildIndex(r.Vault.Path, vault.WalkOptions{AttachmentsDir: r.Profile.Attachments})
	if err != nil {
		return err
	}
	r.sourceIDs = map[string]string{}
	for rel, n := range ix.Notes {
		if n.FM == nil || n.FM.Fields == nil {
			continue
		}
		if sid, ok := n.FM.Fields["source_id"].(string); ok && sid != "" {
			r.sourceIDs[sid] = rel
		}
	}
	return nil
}

// RunSource runs one configured ingester end-to-end: lock + state open,
// pre/post snapshot commits, per-record dedup → write → ack ordering, and
// cursor persistence on clean completion only.
func (r *Runner) RunSource(ctx context.Context, ic config.IngesterConfig) (*Result, error) {
	factory, err := Lookup(ic.Type)
	if err != nil {
		return &Result{Source: ic.Source(), Notes: []string{}}, err
	}
	ing, err := factory(ic.Options)
	if err != nil {
		return &Result{Source: ic.Source(), Notes: []string{}}, fmt.Errorf("ingester %s: %w", ic.Source(), err)
	}
	if d, ok := ing.(noteTypeDefaulter); ok {
		d.SetNoteType(r.Profile.Ingest.Clip)
	}
	if id, ok := ing.(identitySetter); ok {
		id.SetIdentity(r.Vault.Name, ic.Name)
	}
	return r.run(ctx, ic, ing)
}

// storeAssets places rec's assets per the policy (SPEC §31.2), stamps the
// `assets:` frontmatter ledger, and appends the uniform `## Attachments`
// section (SPEC §31.4). No-op for asset-less records.
func (r *Runner) storeAssets(rec *Record) ([]assets.Stored, error) {
	if len(rec.Assets) == 0 {
		return nil, nil
	}
	pol := assets.Policy{
		VaultRoot:      r.Vault.Path,
		AttachmentsDir: r.Profile.Attachments,
		Threshold:      r.Vault.Assets.ThresholdBytes,
		CASDir:         paths.DataDir("assets"),
	}
	stored := make([]assets.Stored, 0, len(rec.Assets))
	for _, a := range rec.Assets {
		s, err := pol.Store(assets.Source{
			Filename:  a.Filename,
			SHA256:    a.SHA256,
			Size:      a.Size,
			Open:      a.Open,
			LocalPath: a.LocalPath,
		})
		if err != nil {
			r.removeNewAssets(stored)
			return nil, fmt.Errorf("store asset %q: %w", a.Filename, err)
		}
		stored = append(stored, s)
	}

	ledger := make([]any, len(stored))
	for i, s := range stored {
		ledger[i] = s.Path
	}
	rec.Fields["assets"] = ledger
	// A bodyless asset note starts straight at ## Attachments — exactly one
	// blank line after the frontmatter fence, never two.
	body := strings.TrimRight(rec.Body, "\n")
	if body == "" {
		rec.Body = attachmentsSection(stored)
	} else {
		rec.Body = body + "\n\n" + attachmentsSection(stored)
	}
	return stored, nil
}

// attachmentsSection renders the uniform `## Attachments` body section:
// vault-relative wikilink embeds (path-qualified — duplicate-basename-safe
// per SPEC §5) for in-vault assets, plain links for external paths. Link
// labels come from the STORED basename (sanitized), never the raw
// emitter-supplied filename — hostile names must not smuggle markup
// (SPEC §28.9 posture).
func attachmentsSection(stored []assets.Stored) string {
	var b strings.Builder
	b.WriteString("## Attachments\n\n")
	for _, s := range stored {
		if s.InVault {
			fmt.Fprintf(&b, "- ![[%s]]\n", s.Path)
			continue
		}
		u := url.URL{Scheme: "file", Path: filepath.ToSlash(s.Path)}
		fmt.Fprintf(&b, "- [%s](%s)\n", profile.SanitizeAssetFilename(filepath.Base(s.Path)), u.String())
	}
	return b.String()
}

// removeNewAssets best-effort deletes assets newly stored for a record
// whose note never landed (SPEC §31.5); reused assets always survive.
func (r *Runner) removeNewAssets(stored []assets.Stored) {
	for _, s := range stored {
		if !s.New {
			continue
		}
		p := s.Path
		if s.InVault {
			p = filepath.Join(r.Vault.Path, filepath.FromSlash(s.Path))
		}
		_ = os.Remove(p)
	}
}

// oneShot replays exactly one pre-built record (push mode, SPEC §19).
type oneShot struct{ rec Record }

func (o oneShot) Name() string { return "adhoc" }
func (o oneShot) Fetch(ctx context.Context, _ Cursor, emit EmitFunc) error {
	return emit(ctx, o.rec)
}

// PushDedupCheck is the §31.5 advisory pre-check: given the NaturalKey a
// push WOULD produce, report whether it is already ingested BEFORE the
// caller downloads the body or runs media hooks — so a re-pushed remote
// video never re-downloads 100 MiB and re-runs a 10-minute transcribe just
// to no-op at emit time. Advisory only: the §17.5 emit-time check stays
// authoritative (this pre-check takes and releases the source flock, so a
// race between it and the real run is resolved there). Any error or a held
// lock returns seen=false — the caller proceeds and the pipeline decides.
func (r *Runner) PushDedupCheck(key string) (existing string, seen bool) {
	if key == "" {
		return "", false
	}
	if r.sourceIDs == nil {
		if err := r.LoadSourceIDs(); err != nil {
			return "", false
		}
	}
	if p, ok := r.sourceIDs[key]; ok {
		return p, true
	}
	statePath := paths.StateDir("state", r.Vault.Name, "adhoc.ndjson")
	st, err := OpenState(statePath, "adhoc", "")
	if err != nil {
		return "", false // lock held or unreadable → let RunPush decide
	}
	defer func() { _ = st.Close() }()
	// Only short-circuit when a real NOTE exists: a key that is "seen"
	// because it was QUARANTINED has no note path, and must flow through
	// the pipeline so the run reports it as deduped, not "already
	// ingested → " with an empty path.
	if p := st.NotePath(key); p != "" {
		return p, true
	}
	return "", false
}

// RunPush runs one record through the full pipeline under the shared
// "adhoc" source, so repeated pushes of the same URL/file dedup.
func (r *Runner) RunPush(ctx context.Context, rec Record) (*Result, error) {
	ic := config.IngesterConfig{Type: "adhoc", Name: "adhoc", Enabled: true,
		Timeout: config.DefaultSourceTimeout, Options: map[string]any{}}
	return r.run(ctx, ic, oneShot{rec: rec})
}

func (r *Runner) run(ctx context.Context, ic config.IngesterConfig, ing Ingester) (*Result, error) {
	res := &Result{Source: ic.Source(), Notes: []string{}}
	if r.sourceIDs == nil {
		if err := r.LoadSourceIDs(); err != nil {
			return res, err
		}
	}

	cursorSchema := ""
	if csp, ok := ing.(cursorSchemaProvider); ok {
		cursorSchema = csp.CursorSchema()
	}
	fileStem := strings.ReplaceAll(ic.Source(), ":", "-")
	statePath := paths.StateDir("state", r.Vault.Name, fileStem+".ndjson")
	st, err := OpenState(statePath, ic.Source(), cursorSchema)
	if err != nil {
		var held lock.ErrHeld
		if errors.As(err, &held) {
			res.AlreadyRunning = true
			return res, nil
		}
		return res, err
	}
	defer func() { _ = st.Close() }()

	op, err := snapshot.Begin(r.Vault, "ingest", r.Now())
	if err != nil {
		return res, err
	}

	quarantineDir := paths.StateDir("failed", r.Vault.Name, fileStem)
	ctx, cancel := context.WithTimeout(ctx, ic.Timeout)
	defer cancel()

	emit := func(ctx context.Context, rec Record) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rec.NaturalKey == "" {
			return fmt.Errorf("ingester %s emitted a record with an empty NaturalKey (ingester bug)", ic.Source())
		}
		if rec.NoteType == "" {
			return fmt.Errorf(`ingester %s emitted a record with no note type; set note_type on the ingester or [ingest] clip in the profile`, ic.Source())
		}
		if st.Seen(rec.NaturalKey) {
			res.Deduped++
			if p := st.NotePath(rec.NaturalKey); p != "" {
				res.Existing = append(res.Existing, p)
			}
			return nil
		}
		if existing, ok := r.sourceIDs[rec.NaturalKey]; ok {
			// Ack repair: a prior run crashed after the note rename but
			// before the ack — the note exists, the ledger doesn't know.
			if err := st.Ack(rec.NaturalKey, existing, r.Now()); err != nil {
				return err
			}
			res.Deduped++
			res.Existing = append(res.Existing, existing)
			return nil
		}
		if _, owned := rec.Fields["source_id"]; owned {
			return fmt.Errorf("ingester %s set source_id itself; the pipeline owns that field (ingester bug)", ic.Source())
		}
		rec.Fields["source_id"] = rec.NaturalKey

		// Assets store BEFORE the note write; a store failure is an
		// execution error — "this machine is bad", never a quarantine
		// (SPEC §31.5).
		stored, err := r.storeAssets(&rec)
		if err != nil {
			return err
		}

		rel, err := writer.Write(r.Vault.Path, r.Profile, rec.NoteType, rec.Fields, rec.Body, quarantineDir, Key(rec.NaturalKey), r.Now())
		if errors.Is(err, writer.ErrQuarantined) {
			// The quarantine file is durable (writer fsyncs) — safe to
			// mark the key seen so re-fetches skip it (SPEC §7). Assets
			// newly stored for this record are removed; reused ones
			// belong to an earlier note (SPEC §31.5).
			r.removeNewAssets(stored)
			if qerr := st.Quarantine(rec.NaturalKey, err.Error(), r.Now()); qerr != nil {
				return qerr
			}
			res.Quarantined++
			return nil
		}
		if err != nil {
			r.removeNewAssets(stored)
			return err
		}
		if err := op.Record(rel); err != nil {
			return err
		}
		// New in-vault attachments ride the op journal so `pkms undo`
		// removes a note and its attachments together; external paths are
		// outside the vault and outside git (SPEC §31.5).
		for _, s := range stored {
			if s.InVault && s.New {
				if err := op.Record(s.Path); err != nil {
					return err
				}
			}
		}
		if err := st.Ack(rec.NaturalKey, rel, r.Now()); err != nil {
			return err
		}
		r.sourceIDs[rec.NaturalKey] = rel
		res.New++
		res.Notes = append(res.Notes, rel)
		return nil
	}

	cursor := st.Cursor()
	if cursor == nil {
		cursor = Cursor{}
	}
	fetchErr := ing.Fetch(ctx, cursor, emit)
	if cr, ok := ing.(CursorResetter); ok && cr.CursorWasReset() {
		res.CursorReset = true
	}
	if dr, ok := ing.(dropReporter); ok {
		res.Dropped = dr.DroppedItems()
	}

	// Commit the acked notes FIRST — they are durable, so a later cursor
	// or commit hiccup must never leave them uncommitted (undo depends on
	// the op journal). The cursor is only persisted after a clean Fetch:
	// on error the next run resumes from the old cursor and dedup no-ops
	// the overlap (SPEC §17 step 6).
	if res.New == 0 {
		if err := op.Discard(); err != nil {
			return res, err
		}
	} else if err := op.End(res.Summary()); err != nil {
		return res, err
	}
	if fetchErr == nil && len(cursor) > 0 {
		if err := st.SetCursor(cursor, r.Now()); err != nil {
			return res, err
		}
	}
	if fetchErr != nil {
		return res, fmt.Errorf("ingester %s: %w", ic.Source(), fetchErr)
	}
	return res, nil
}
