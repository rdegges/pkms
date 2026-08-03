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
	"time"

	"github.com/goccy/go-yaml"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// ErrQuarantined marks a record rejected by schema validation.
var ErrQuarantined = errors.New("record quarantined")

// Write creates one new note in the vault. Returns the vault-relative path
// actually written (collisions get deterministic " 2", " 3"… suffixes).
// On schema failure the record is written to quarantineDir as JSON and
// ErrQuarantined is returned (wrapped, with the quarantine file path).
func Write(vaultRoot string, prof *profile.Profile, noteType string,
	fields map[string]any, body string, quarantineDir string, now time.Time) (string, error) {

	if sch := prof.Schema(noteType); sch != nil {
		if err := sch.Validate(fields); err != nil {
			qPath, qErr := quarantine(quarantineDir, noteType, fields, body, err, now)
			if qErr != nil {
				return "", fmt.Errorf("schema validation failed AND quarantine failed: %v / %w", err, qErr)
			}
			return "", fmt.Errorf("%w: %s (%v)", ErrQuarantined, qPath, err)
		}
	}

	folder, filename, err := prof.RenderPath(noteType, fields)
	if err != nil {
		return "", err
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
		return "", err
	}
	abs, err := vault.CreateNewNote(destDir, filename, content)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(vaultRoot, abs)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
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

func quarantine(dir, noteType string, fields map[string]any, body string, verr error, now time.Time) (string, error) {
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
	f, err := os.CreateTemp(dir, now.UTC().Format("20060102T150405Z")+"-*.json")
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

func syncQuarantineDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
