// Package query implements deterministic retrieval over the vault index:
// field filters + full-text + backlinks, AND-combined (SPEC §10).
package query

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

type Options struct {
	Type      string
	Where     map[string]string
	Text      string
	Backlinks string
	Orphans   bool
}

type Result struct {
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
}

// Run evaluates all predicates (AND) over every note, in path order.
func Run(ix *vault.Index, prof *profile.Profile, opts Options) []Result {
	var backlinkSources map[string]bool
	if opts.Backlinks != "" {
		backlinkSources = map[string]bool{}
		for _, ref := range ix.Backlinks[opts.Backlinks] {
			backlinkSources[ref.Source] = true
		}
	}

	var out []Result
	for _, p := range ix.NotePaths() {
		n := ix.Notes[p]
		if opts.Type != "" {
			var fields map[string]any
			if n.FM != nil {
				fields = n.FM.Fields
			}
			if prof.TypeOf(p, fields) != opts.Type {
				continue
			}
		}
		if !matchWhere(n, opts.Where) {
			continue
		}
		if opts.Text != "" &&
			!bytes.Contains(bytes.ToLower(n.Body), []byte(strings.ToLower(opts.Text))) {
			continue
		}
		if backlinkSources != nil && !backlinkSources[p] {
			continue
		}
		if opts.Orphans && inboundCount(ix, p) > 0 {
			continue
		}
		r := Result{Path: p}
		if n.FM != nil && n.FM.Fields != nil {
			r.Frontmatter = n.FM.Fields
		}
		out = append(out, r)
	}
	return out
}

func inboundCount(ix *vault.Index, p string) int {
	count := 0
	for _, ref := range ix.Backlinks[p] {
		if ref.Source != p {
			count++
		}
	}
	return count
}

func matchWhere(n *vault.Note, where map[string]string) bool {
	if len(where) == 0 {
		return true
	}
	if n.FM == nil || n.FM.Fields == nil {
		return false
	}
	for key, want := range where {
		v, ok := lookupPath(n.FM.Fields, strings.Split(key, "."))
		if !ok || !valueMatches(v, want) {
			return false
		}
	}
	return true
}

func lookupPath(fields map[string]any, path []string) (any, bool) {
	var cur any = fields
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// valueMatches compares scalars by normalized string form; lists match
// when any element matches ("contains" semantics — SPEC §10).
func valueMatches(v any, want string) bool {
	switch t := v.(type) {
	case []any:
		for _, e := range t {
			if scalarString(e) == want {
				return true
			}
		}
		return false
	default:
		return scalarString(v) == want
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}
