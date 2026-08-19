package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	lint.Register("file-too-large", func(cfg map[string]any) (any, error) {
		return fileTooLarge{}, nil
	})
	lint.Register("frontmatter-present", func(cfg map[string]any) (any, error) {
		warn, err := lint.CfgStrings(cfg, "warning_types")
		if err != nil {
			return nil, err
		}
		return fmPresent{warningTypes: warn}, nil
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
		keys, err := lint.CfgStrings(cfg, "keys")
		if err != nil {
			return nil, err
		}
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
	// Slug is quoted: an unquoted "foo #bar" would YAML-parse as "foo".
	block := fmt.Sprintf("---\ndate: %s\nslug: %q\ntags:\n  - session-trace\n---\n", m[1], m[2])
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

// yamlishLine: lines that structurally belong to a YAML mapping block —
// a top-level key, a list item, or an indented continuation.
var yamlishLine = regexp.MustCompile(`^([A-Za-z0-9_-]+:|\s*- |\s+\S)`)

// Fix inserts the closing fence before the first line that does not look
// like YAML mapping content. Blank lines, markdown headings and prose end
// the block even though YAML would tolerate them (as comments/scalars) —
// the longest-parseable-prefix approach swallowed body content into the
// frontmatter, which is a semantic guess, not an unambiguous repair.
// Report-only unless the resulting prefix parses as a non-empty mapping.
func (fmClosed) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	if n.FM == nil || !n.FM.Unclosed {
		return nil, nil
	}
	lines := srcLines(n.Src) // line 1 is the opening fence
	if len(lines) > 200 {
		return nil, nil
	}
	end := 0 // last line index (0-based) that is yamlish
	prevOpensBlock := false
	for k := 1; k < len(lines); k++ {
		line := bytes.TrimSuffix(lines[k], []byte("\n"))
		if len(bytes.TrimSpace(line)) == 0 || !yamlishLine.Match(line) {
			break
		}
		// Indented continuations are only YAML when the previous line
		// opened a block (key:, list item, or block scalar) — otherwise
		// an indented BODY line right after the mapping would be absorbed
		// into the frontmatter (codex finding).
		indented := line[0] == ' ' || line[0] == '\t'
		if indented && !prevOpensBlock {
			break
		}
		trimmed := bytes.TrimSpace(line)
		prevOpensBlock = bytes.HasSuffix(trimmed, []byte(":")) ||
			bytes.HasSuffix(trimmed, []byte("|")) || bytes.HasSuffix(trimmed, []byte(">")) ||
			bytes.HasPrefix(trimmed, []byte("- "))
		end = k
	}
	if end == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := yaml.Unmarshal(joinLines(lines[1:end+1]), &m); err != nil || len(m) == 0 {
		return nil, nil
	}
	newSrc, ok := insertLineAfter(n.Src, end+1, []byte("---"))
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
		if _, err := time.Parse(time.RFC3339, raw); err == nil {
			// The ingest pipeline stamps `created` as a full RFC3339
			// timestamp (SPEC §20) — a valid timestamp is not a malformed
			// date (SPEC §31.10; latent phase-2 conflict).
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
