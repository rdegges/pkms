package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("file-too-large", func(cfg map[string]any) (any, error) {
		return fileTooLarge{}, nil
	})
	lint.Register("frontmatter-present", func(cfg map[string]any) (any, error) {
		return fmPresent{warningTypes: cfgStrings(cfg, "warning_types")}, nil
	})
	lint.Register("frontmatter-closed", func(cfg map[string]any) (any, error) {
		return fmClosed{}, nil
	})
	lint.Register("frontmatter-valid-yaml", func(cfg map[string]any) (any, error) {
		return fmValidYAML{}, nil
	})
	lint.Register("frontmatter-no-tabs", func(cfg map[string]any) (any, error) {
		return fmNoTabs{}, nil
	})
	lint.Register("date-format-iso", func(cfg map[string]any) (any, error) {
		keys := cfgStrings(cfg, "keys")
		if len(keys) == 0 {
			keys = []string{"date", "created", "updated", "last_updated", "published"}
		}
		return dateFormatISO{keys: keys}, nil
	})
}

// ---- file-too-large -------------------------------------------------------

type fileTooLarge struct{}

func (fileTooLarge) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if !n.TooLarge {
		return nil
	}
	return []lint.Finding{finding(lint.Warning, n.RelPath, 0, false,
		"body exceeds %d bytes; skipped markdown analysis", vault.MaxBodyParseSize)}
}

// ---- frontmatter-present ---------------------------------------------------

type fmPresent struct {
	warningTypes []string
}

func (r fmPresent) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if n.FM != nil {
		return nil
	}
	typ := typedNote(ctx, n)
	if typ == "" || ctx.Prof.Schema(typ) == nil {
		return nil // untyped/schema-less notes don't require frontmatter
	}
	sev := lint.Error
	if contains(r.warningTypes, typ) {
		sev = lint.Warning
	}
	fixable := typ == "session-trace" && traceFilenameRe.MatchString(baseOf(n.RelPath))
	return []lint.Finding{finding(sev, n.RelPath, 1, fixable,
		"%s note has no frontmatter block", typ)}
}

// traceFilenameRe: "YYYY-MM-DD — slug.md" (em dash).
var traceFilenameRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) — (.+)\.md$`)

func baseOf(rel string) string {
	if i := strings.LastIndexByte(rel, '/'); i >= 0 {
		return rel[i+1:]
	}
	return rel
}

// Fix creates the frontmatter block for a session trace with date/slug
// derived from its conforming filename (docs/LINT-RULES.md).
func (r fmPresent) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	m := traceFilenameRe.FindStringSubmatch(baseOf(n.RelPath))
	if m == nil || n.FM != nil {
		return nil, nil
	}
	block := fmt.Sprintf("---\ndate: %s\nslug: %s\ntags:\n  - session-trace\n---\n", m[1], m[2])
	return &lint.FixResult{NewSrc: append([]byte(block), n.Src...)}, nil
}

// ---- frontmatter-closed ----------------------------------------------------

type fmClosed struct{}

func (fmClosed) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if n.FM == nil || !n.FM.Unclosed {
		return nil
	}
	return []lint.Finding{finding(lint.Error, n.RelPath, 1, true,
		"frontmatter block has no closing --- fence")}
}

// Fix inserts the closing fence after the longest prefix of lines that
// still parses as a YAML mapping (bounded to the first 100 lines).
func (fmClosed) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if n.FM == nil || !n.FM.Unclosed {
		return nil, nil
	}
	lines := srcLines(n.Src) // line 1 is the opening fence
	limit := len(lines)
	if limit > 100 {
		return nil, nil // unbounded scan → report-only
	}
	best := 0
	for k := 1; k < limit; k++ {
		prefix := joinLines(lines[1 : k+1])
		var m map[string]any
		if err := yaml.Unmarshal(prefix, &m); err == nil && m != nil {
			best = k
		}
	}
	if best == 0 {
		return nil, nil
	}
	newSrc, ok := insertLineAfter(n.Src, best+1, []byte("---"))
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

// ---- frontmatter-valid-yaml -------------------------------------------------

type fmValidYAML struct{}

func (fmValidYAML) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if n.FM == nil || n.FM.Unclosed || n.FM.ParseErr == nil {
		return nil
	}
	msg := strings.SplitN(n.FM.ParseErr.Error(), "\n", 2)[0]
	return []lint.Finding{finding(lint.Error, n.RelPath, 2, false,
		"frontmatter is not valid YAML: %s", msg)}
}

// ---- frontmatter-no-tabs ------------------------------------------------------

type fmNoTabs struct{}

func (fmNoTabs) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if n.FM == nil || !bytes.Contains(n.FM.Inner, []byte("\t")) {
		return nil
	}
	// Fixable when all tabs are leading indentation.
	fixable := true
	for _, l := range bytes.Split(n.FM.Inner, []byte("\n")) {
		trimmed := bytes.TrimLeft(l, "\t ")
		if bytes.Contains(trimmed, []byte("\t")) {
			fixable = false
			break
		}
	}
	return []lint.Finding{finding(lint.Error, n.RelPath, 2, fixable,
		"frontmatter contains tab indentation (YAML forbids tabs)")}
}

func (fmNoTabs) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if n.FM == nil {
		return nil, nil
	}
	lines := srcLines(n.Src)
	end := n.FM.EndLine
	if n.FM.Unclosed || end > len(lines) {
		end = len(lines)
	}
	changed := false
	for i := 1; i < end; i++ { // skip opening fence, stop before closing
		l := lines[i]
		for len(l) > 0 {
			trimmed := bytes.TrimLeft(l, " ")
			if len(trimmed) == 0 || trimmed[0] != '\t' {
				break
			}
			l = bytes.Replace(l, []byte("\t"), []byte("  "), 1)
			changed = true
		}
		lines[i] = l
	}
	if !changed {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: joinLines(lines)}, nil
}

// ---- date-format-iso -----------------------------------------------------------

type dateFormatISO struct {
	keys []string
}

func (r dateFormatISO) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	if n.FM == nil || n.FM.Fields == nil {
		return nil
	}
	var out []lint.Finding
	for _, key := range r.keys {
		v, present := n.FM.Fields[key]
		if !present {
			continue
		}
		line := n.FM.Lines[key]
		if v == nil {
			out = append(out, finding(lint.Error, n.RelPath, line, false,
				"%s: empty/null date (want YYYY-MM-DD)", key))
			continue
		}
		raw := n.FM.RawScalars[key]
		if raw == "" {
			if s, ok := v.(string); ok {
				raw = s
			} else {
				out = append(out, finding(lint.Error, n.RelPath, line, false,
					"%s: %v is not a date string (want YYYY-MM-DD)", key, v))
				continue
			}
		}
		if isISODate(raw) {
			continue
		}
		_, fixable := reformatDate(raw)
		out = append(out, finding(lint.Error, n.RelPath, line, fixable,
			"%s: %q is not a valid YYYY-MM-DD date", key, raw))
	}
	return out
}

var dateMsgKeyRe = regexp.MustCompile(`^([A-Za-z0-9_]+): `)

func (r dateFormatISO) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if n.FM == nil {
		return nil, nil
	}
	m := dateMsgKeyRe.FindStringSubmatch(f.Message)
	if m == nil {
		return nil, nil
	}
	key := m[1]
	raw := n.FM.RawScalars[key]
	iso, ok := reformatDate(raw)
	if !ok {
		return nil, nil
	}
	line, ok := getLine(n.Src, f.Line)
	if !ok || !bytes.Contains(line, []byte(raw)) {
		return nil, nil
	}
	newLine := bytes.Replace(line, []byte(raw), []byte(iso), 1)
	newSrc, ok := replaceLine(n.Src, f.Line, newLine)
	if !ok {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}
