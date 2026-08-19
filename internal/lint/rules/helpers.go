// Package rules implements the lint rule catalog (docs/LINT-RULES.md).
// Each rule is registered in init(); config comes from the profile's
// [lint.<id>] table merged with per-vault overrides.
package rules

import (
	"bytes"
	"fmt"
	"regexp"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

// ---- config readers ----------------------------------------------------

func cfgString(cfg map[string]any, key, def string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return def
}

func cfgInt(cfg map[string]any, key string, def int) int {
	switch v := cfg[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	case float64:
		return int(v)
	}
	return def
}

func cfgBool(cfg map[string]any, key string, def bool) bool {
	if v, ok := cfg[key].(bool); ok {
		return v
	}
	return def
}

func cfgRegexps(cfg map[string]any, key string) ([]*regexp.Regexp, error) {
	pats, err := lint.CfgStrings(cfg, key)
	if err != nil {
		return nil, err
	}
	var out []*regexp.Regexp
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// globProbes is a fixed corpus fed to doublestar.Match at construction time.
// ValidatePattern alone is not enough: doublestar re-validates a pattern
// SUFFIX when matching stops early, and a suffix that splits a character
// class holding '{' or '}' fails there (e.g. "{[}]}", "[!a{b]"). The corpus
// is a heuristic; FuzzAcceptedScopeGlobIsMatchable keeps it strict enough.
var globProbes = []string{"", "a", "a/b", "a/b/c", "A"}

// validGlobs rejects malformed doublestar patterns at construction time so
// a bad scope can never silently match nothing at check time (issue #30).
func validGlobs(key string, globs []string) error {
	for _, g := range globs {
		if !doublestar.ValidatePattern(g) {
			return fmt.Errorf("%s: malformed glob %q", key, g)
		}
		for _, probe := range globProbes {
			if _, err := doublestar.Match(g, probe); err != nil {
				return fmt.Errorf("%s: malformed glob %q: %v", key, g, err)
			}
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func matchAnyGlob(globs []string, rel string) bool {
	for _, g := range globs {
		if ok, err := doublestar.Match(g, rel); err == nil && ok {
			return true
		}
	}
	return false
}

// ---- date helpers -------------------------------------------------------

var isoDateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func isISODate(s string) bool {
	if !isoDateRe.MatchString(s) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// reformatDate converts unambiguous alternative spellings to ISO.
// Ambiguous forms (05/07/2026) intentionally have no entry here.
var altDateLayouts = []string{
	"2006/01/02", "2006.01.02", "January 2, 2006", "Jan 2, 2006", "2006-1-2",
}

func reformatDate(raw string) (string, bool) {
	for _, layout := range altDateLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Format("2006-01-02"), true
		}
	}
	return "", false
}

// ---- raw line edits (minimal-diff fixes; never re-marshal YAML) ---------

func srcLines(src []byte) [][]byte {
	return bytes.SplitAfter(src, []byte("\n"))
}

func joinLines(lines [][]byte) []byte {
	return bytes.Join(lines, nil)
}

// replaceLine swaps 1-based line lineNo for newContent (newline preserved).
func replaceLine(src []byte, lineNo int, newContent []byte) ([]byte, bool) {
	lines := srcLines(src)
	if lineNo < 1 || lineNo > len(lines) {
		return nil, false
	}
	old := lines[lineNo-1]
	nl := []byte(nil)
	if bytes.HasSuffix(old, []byte("\n")) {
		nl = []byte("\n")
	}
	lines[lineNo-1] = append(append([]byte(nil), newContent...), nl...)
	return joinLines(lines), true
}

// insertLineAfter inserts content (with trailing newline) after 1-based
// lineNo; lineNo 0 prepends.
func insertLineAfter(src []byte, lineNo int, content []byte) ([]byte, bool) {
	lines := srcLines(src)
	if lineNo < 0 || lineNo > len(lines) {
		return nil, false
	}
	ins := append(append([]byte(nil), content...), '\n')
	out := make([][]byte, 0, len(lines)+1)
	out = append(out, lines[:lineNo]...)
	out = append(out, ins)
	out = append(out, lines[lineNo:]...)
	return joinLines(out), true
}

// getLine returns the 1-based line without its newline.
func getLine(src []byte, lineNo int) ([]byte, bool) {
	lines := srcLines(src)
	if lineNo < 1 || lineNo > len(lines) {
		return nil, false
	}
	return bytes.TrimSuffix(lines[lineNo-1], []byte("\n")), true
}

// ---- misc ----------------------------------------------------------------

func finding(sev lint.Severity, path string, line int, fixable bool, format string, a ...any) lint.Finding {
	return lint.Finding{
		Severity: sev, Path: path, Line: line, Fixable: fixable,
		Message: fmt.Sprintf(format, a...),
	}
}

// typedNote reports the profile type; "" = unclassified.
func typedNote(ctx *lint.Context, n *vault.Note) string { return ctx.TypeOf(n) }

// levenshtein computes edit distance (for single-repair link fixes).
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
