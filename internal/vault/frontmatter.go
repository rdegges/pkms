package vault

import (
	"bytes"
	"fmt"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// Frontmatter is the parsed leading `---` block of a note.
//
// Obsidian semantics: the block must start at byte 0 with a line that is
// exactly `---`, and ends at the next line that is exactly `---` (or `...`).
// Mutating fixes are raw line edits on the note source — never a re-marshal —
// because YAML re-serialization normalizes quoting (verified: goccy quotes
// bare dates on output), which would violate the minimal-diff invariant.
type Frontmatter struct {
	// Inner is the raw YAML between the fences (no fence lines).
	Inner []byte
	// Fields is the typed top-level mapping (values converted for JSON
	// Schema validation: ordered maps -> map[string]any, integers -> int64).
	Fields map[string]any
	// Order is the top-level key order as written.
	Order []string
	// Lines maps top-level keys to their 1-based line number in the file.
	Lines map[string]int
	// RawScalars maps top-level keys to the raw scalar token text as
	// written (e.g. `2026/07/15`), for format rules that must see the
	// original spelling. Only set for scalar values.
	RawScalars map[string]string
	// EndLine is the 1-based line number of the closing fence.
	EndLine int
	// Unclosed is true when the opening fence has no closing fence
	// (Fields will be nil; the whole file was treated as frontmatter).
	Unclosed bool
	// ParseErr holds the YAML parse error, if any (Fields will be nil).
	ParseErr error
}

var (
	fenceOpen  = []byte("---")
	fenceClose = []byte("...")
)

// MaxFrontmatterSize rejects absurd frontmatter blocks before YAML parsing
// (parser resource limit — SPEC §14).
const MaxFrontmatterSize = 64 << 10

// splitFrontmatter splits src into (frontmatter, body, bodyOffset).
// fm is nil when the file has no frontmatter block at byte 0.
func splitFrontmatter(src []byte) (fm *Frontmatter, body []byte, bodyOffset int) {
	first, rest, ok := cutLine(src)
	if !ok || !bytes.Equal(trimCR(first), fenceOpen) {
		return nil, src, 0
	}
	line := 2
	inner := rest
	for cur := rest; ; line++ {
		l, next, more := cutLine(cur)
		t := trimCR(l)
		if bytes.Equal(t, fenceOpen) || bytes.Equal(t, fenceClose) {
			innerLen := len(inner) - len(cur)
			f := parseFrontmatterInner(inner[:innerLen])
			f.EndLine = line
			bodyOff := len(src) - len(next)
			return f, src[bodyOff:], bodyOff
		}
		if !more {
			// No closing fence anywhere.
			return &Frontmatter{Inner: inner, Unclosed: true}, nil, len(src)
		}
		cur = next
	}
}

func parseFrontmatterInner(inner []byte) *Frontmatter {
	f := &Frontmatter{
		Inner:      inner,
		Lines:      map[string]int{},
		RawScalars: map[string]string{},
	}
	if len(inner) > MaxFrontmatterSize {
		f.ParseErr = fmt.Errorf("frontmatter exceeds %d bytes; not parsed", MaxFrontmatterSize)
		return f
	}
	var ms yaml.MapSlice
	if err := yaml.UnmarshalWithOptions(inner, &ms, yaml.UseOrderedMap()); err != nil {
		f.ParseErr = err
		return f
	}
	f.Fields = make(map[string]any, len(ms))
	for _, it := range ms {
		key := fmt.Sprintf("%v", it.Key)
		f.Order = append(f.Order, key)
		f.Fields[key] = normalizeYAML(it.Value)
	}
	// Positions + raw scalar spellings come from the AST pass.
	if file, err := parser.ParseBytes(inner, 0); err == nil && len(file.Docs) > 0 {
		if m, ok := file.Docs[0].Body.(*ast.MappingNode); ok {
			for _, kv := range m.Values {
				key := kv.Key.String()
				if tk := kv.Key.GetToken(); tk != nil {
					// +1: inner starts on file line 2 (after the fence).
					f.Lines[key] = tk.Position.Line + 1
				}
				if v, ok := kv.Value.(*ast.StringNode); ok {
					f.RawScalars[key] = v.Value
				} else if kv.Value != nil && isScalarNode(kv.Value) {
					if tk := kv.Value.GetToken(); tk != nil {
						f.RawScalars[key] = tk.Value
					}
				}
			}
		}
	}
	return f
}

func isScalarNode(n ast.Node) bool {
	switch n.(type) {
	case *ast.StringNode, *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.NullNode:
		return true
	}
	return false
}

// normalizeYAML converts goccy decode output to JSON-Schema-friendly values:
// yaml.MapSlice -> map[string]any, uint64/int -> int64, recursively.
func normalizeYAML(v any) any {
	switch t := v.(type) {
	case yaml.MapSlice:
		m := make(map[string]any, len(t))
		for _, it := range t {
			m[fmt.Sprintf("%v", it.Key)] = normalizeYAML(it.Value)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = normalizeYAML(val)
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeYAML(e)
		}
		return out
	case uint64:
		return int64(t)
	case int:
		return int64(t)
	case time.Time:
		return t.Format("2006-01-02")
	default:
		return v
	}
}

// StringList reads a key that Obsidian accepts as string-or-list
// (tags, aliases) and returns it normalized to a list.
// ok is false when the key is absent or not a string/list-of-strings.
func (f *Frontmatter) StringList(key string) (vals []string, wasString bool, ok bool) {
	raw, present := f.Fields[key]
	if !present || raw == nil {
		return nil, false, false
	}
	switch t := raw.(type) {
	case string:
		return []string{t}, true, true
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, isStr := e.(string)
			if !isStr {
				return nil, false, false
			}
			out = append(out, s)
		}
		return out, false, true
	}
	return nil, false, false
}

// cutLine splits b at the first newline. more is false on the last line.
func cutLine(b []byte) (line, rest []byte, more bool) {
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		return b[:i], b[i+1:], true
	}
	return b, nil, false
}

func trimCR(b []byte) []byte {
	return bytes.TrimSuffix(b, []byte("\r"))
}
