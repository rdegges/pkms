package rules

import (
	"path/filepath"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("note-valid-text", func(cfg map[string]any) (any, error) {
		return noteValidText{}, nil
	})
}

// ---- note-valid-text (SPEC §33) ----------------------------------------------

// noteValidText: a note must be valid UTF-8 with no control bytes. It scans
// the raw bytes (frontmatter included) and deliberately covers TooLarge
// notes — corruption in a big file must not pass by omission. At most two
// findings per note (one per defect kind), so a binary file misnamed .md
// cannot flood the report.
type noteValidText struct{}

func (noteValidText) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	var rep vault.TextReport
	if n.TooLarge {
		// Over-cap notes are indexed without their bytes (SPEC §14), so
		// stream-scan from disk. An unreadable note cannot be proven valid:
		// fail closed with an error finding, never silently pass.
		var err error
		rep, err = vault.ScanTextFile(filepath.Join(ctx.Ix.Root, filepath.FromSlash(n.RelPath)))
		if err != nil {
			// %q: the error embeds the filename (*fs.PathError), which can
			// carry the very control bytes this rule catches.
			return []lint.Finding{finding(lint.Error, n.RelPath, 0, false,
				"could not read note for text validation: %q", err.Error())}
		}
	} else {
		rep = vault.ScanText(n.Src)
	}
	var out []lint.Finding
	if rep.InvalidUTF8 > 0 {
		out = append(out, finding(lint.Error, n.RelPath, rep.FirstInvalidLine, false,
			"note is not valid UTF-8 (%d invalid byte(s))", rep.InvalidUTF8))
	}
	if rep.ControlBytes > 0 {
		out = append(out, finding(lint.Error, n.RelPath, rep.FirstControlLine, false,
			"note contains %d control byte(s) (first: 0x%02X)", rep.ControlBytes, rep.FirstControl))
	}
	return out
}
