// Package writer is the single write path for new notes (SPEC §6):
// validate against the note-type schema → render placement templates →
// marshal frontmatter → atomic write. Schema failures quarantine OUTSIDE
// the vault so malformed or sensitive raw content never syncs.
package writer

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// ErrQuarantined marks a record rejected by schema validation.
var ErrQuarantined = errors.New("record quarantined")

// Write creates one new note in the vault. Returns the vault-relative path
// actually written (collisions get deterministic " 2", " 3"… suffixes).
// On schema failure the record is written to quarantineDir as JSON —
// named <ts>-<keyHash>.json (SPEC §2) when keyHash is non-empty — and
// ErrQuarantined is returned (wrapped, with the quarantine file path).
func Write(vaultRoot string, prof *profile.Profile, noteType string,
	fields map[string]any, body string, quarantineDir, keyHash string, now time.Time) (string, error) {

	if sch := prof.Schema(noteType); sch != nil {
		if err := sch.Validate(fields); err != nil {
			return quarantineReject(quarantineDir, keyHash, noteType, fields, body, err, now)
		}
	}

	// A record whose validated fields still can't render a legal path
	// (bad template output, a title that sanitizes to empty) is a
	// deterministic REJECT, not a pipeline failure: quarantine it so the
	// batch continues and it is never retried (SPEC §17 step 5e).
	folder, filename, err := prof.RenderPath(noteType, fields)
	if err != nil {
		return quarantineReject(quarantineDir, keyHash, noteType, fields, body, err, now)
	}

	destDir := filepath.Join(vaultRoot, filepath.FromSlash(folder))
	// Symlink containment (SPEC §14): the RESOLVED destination must stay
	// inside the RESOLVED vault root — lexical checks in RenderPath don't
	// catch a symlinked directory pointing outside the vault. Check the
	// deepest EXISTING ancestor BEFORE MkdirAll, or the mkdir itself would
	// create directories through the symlink, outside the vault.
	anc := destDir
	for {
		if _, err := os.Lstat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			break
		}
		anc = parent
	}
	if err := confined(vaultRoot, anc); err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	if err := confined(vaultRoot, destDir); err != nil {
		return "", err
	}

	content, err := renderNote(prof, noteType, fields, body)
	if err != nil {
		// A hostile string value the YAML emitter can't round-trip
		// (e.g. a title of "? x" that reparses to zero fields, or ".inf"
		// that reparses as a number) is a property of the record —
		// quarantine it rather than write silently-corrupt frontmatter
		// (SPEC §6 round-trip invariant).
		if errors.Is(err, errFrontmatterRoundTrip) {
			return quarantineReject(quarantineDir, keyHash, noteType, fields, body, err, now)
		}
		return "", err
	}
	abs, err := vault.CreateNewNote(destDir, filename, content)
	if err != nil {
		// ENAMETOOLONG is a property of the record (a pathological title),
		// not the environment — quarantine so it never wedges the batch.
		// Genuine IO failures (ENOSPC, EACCES) still propagate and abort.
		if errors.Is(err, syscall.ENAMETOOLONG) {
			return quarantineReject(quarantineDir, keyHash, noteType, fields, body, err, now)
		}
		return "", err
	}
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// quarantineReject writes the rejected record to the quarantine dir and
// returns ErrQuarantined wrapping the reason; used for every
// record-deterministic rejection (schema, path render, filename length).
func quarantineReject(dir, keyHash, noteType string, fields map[string]any, body string, reason error, now time.Time) (string, error) {
	qPath, qErr := quarantine(dir, keyHash, noteType, fields, body, reason, now)
	if qErr != nil {
		return "", fmt.Errorf("record rejected (%v) AND quarantine failed: %w", reason, qErr)
	}
	return "", fmt.Errorf("%w: %s (%v)", ErrQuarantined, qPath, reason)
}

func confined(root, dir string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if realDir != realRoot && !strings.HasPrefix(realDir, realRoot+string(filepath.Separator)) {
		return fmt.Errorf("destination %s escapes the vault (resolves to %s)", dir, realDir)
	}
	return nil
}

// renderNote marshals frontmatter (sorted keys — deterministic) and
// appends the body, via the type's body template when one is declared.
func renderNote(prof *profile.Profile, noteType string, fields map[string]any, body string) ([]byte, error) {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ms := make(yaml.MapSlice, 0, len(fields))
	for _, k := range keys {
		ms = append(ms, yaml.MapItem{Key: k, Value: fields[k]})
	}
	fm, err := yaml.MarshalWithOptions(ms, yaml.Indent(2), yaml.IndentSequence(true))
	if err != nil {
		return nil, err
	}
	if err := verifyRoundTrip(fields, fm); err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString("---\n")
	out.Write(fm)
	out.WriteString("---\n")
	if !strings.HasPrefix(body, "\n") && body != "" {
		out.WriteString("\n")
	}
	out.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		out.WriteString("\n")
	}
	return []byte(out.String()), nil
}

// errFrontmatterRoundTrip marks a record whose marshaled frontmatter does
// not parse back to the same fields (a YAML-emitter edge case on a hostile
// value); the record is quarantined, never written.
var errFrontmatterRoundTrip = errors.New("frontmatter does not round-trip")

// verifyRoundTrip reparses the marshaled frontmatter and confirms every
// input field survives with an equal value (SPEC §6). This is the backstop
// against emitter gaps — a title of "? x" that reparses to no fields, an
// ".inf"/".nan" that reparses as a number, a tab that is silently dropped.
func verifyRoundTrip(fields map[string]any, fm []byte) error {
	var got map[string]any
	if err := yaml.Unmarshal(fm, &got); err != nil {
		return fmt.Errorf("%w: reparse failed (%v)", errFrontmatterRoundTrip, err)
	}
	for k, v := range fields {
		gv, ok := got[k]
		if !ok {
			return fmt.Errorf("%w: field %q vanished on reparse", errFrontmatterRoundTrip, k)
		}
		// Strings are the injection surface; compare them exactly. Other
		// scalar/list types are trusted (schema-validated, pkms-built).
		if s, isStr := v.(string); isStr {
			if gs, ok := gv.(string); !ok || gs != s {
				return fmt.Errorf("%w: field %q changed on reparse (%q → %v)", errFrontmatterRoundTrip, k, s, gv)
			}
		}
	}
	return nil
}

func quarantine(dir, keyHash, noteType string, fields map[string]any, body string, verr error, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	payload := map[string]any{
		"note_type": noteType,
		"record":    map[string]any{"fields": fields, "body": body},
		"errors":    verr.Error(),
		"ts":        now.UTC().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	f, err := createQuarantineFile(dir, keyHash, now)
	if err != nil {
		return "", err
	}
	// The quarantine file may be the ONLY copy of the rejected record once
	// the pipeline acks the key — it must be durable before we return.
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := syncQuarantineDir(dir); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// createQuarantineFile opens <ts>-<keyHash>.json with O_EXCL (per-source
// locks make collisions a same-second re-run bug, not a race — suffix
// deterministically). Without a key (direct writer callers) fall back to
// a random-suffix temp name.
func createQuarantineFile(dir, keyHash string, now time.Time) (*os.File, error) {
	ts := now.UTC().Format("20060102T150405Z")
	if keyHash == "" {
		return os.CreateTemp(dir, ts+"-*.json")
	}
	base := ts + "-" + keyHash
	for i := 1; i <= 100; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		f, err := os.OpenFile(filepath.Join(dir, name+".json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("cannot allocate quarantine file for %s in %s", base, dir)
}

func syncQuarantineDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
