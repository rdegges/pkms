package rules

import (
	"fmt"
	"sort"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

func init() {
	// Off by default (docs/LINT-RULES.md): requires explicit enabled=true
	// AND per-type key orders in config, e.g.
	//   [lint.frontmatter-key-order]
	//   enabled = true
	//   [lint.frontmatter-key-order.orders]
	//   meeting = ["date", "time", "duration", "type", ...]
	lint.Register("frontmatter-key-order", func(cfg map[string]any) (any, error) {
		enabled, err := lint.CfgBool(cfg, "enabled", false)
		if err != nil {
			return nil, err
		}
		if !enabled {
			return nil, nil
		}
		raw, ok := cfg["orders"]
		if !ok {
			return nil, nil
		}
		table, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("orders: got %T, want a table of per-type key lists", raw)
		}
		if len(table) == 0 {
			return nil, nil
		}
		orders := map[string][]string{}
		types := make([]string, 0, len(table))
		for typ := range table {
			types = append(types, typ)
		}
		// Sorted so a config error names the same type on every run.
		sort.Strings(types)
		for _, typ := range types {
			ss, err := lint.CfgStrings(table, typ)
			if err != nil {
				return nil, fmt.Errorf("orders.%w", err)
			}
			orders[typ] = ss
		}
		return keyOrder{orders: orders}, nil
	})
}

type keyOrder struct {
	orders map[string][]string
}

// rank returns the template position of a key, -1 for unknown keys
// (unknown keys are allowed anywhere after the known prefix).
func rank(order []string, key string) int {
	for i, k := range order {
		if k == key {
			return i
		}
	}
	return -1
}

func (r keyOrder) CheckNote(ctx *lint.Context, n *vault.Note) []lint.Finding {
	order, ok := r.orders[typedNote(ctx, n)]
	if !ok || !hasParsedFM(n) {
		return nil
	}
	last := -1
	for _, key := range n.FM.Order {
		pos := rank(order, key)
		if pos < 0 {
			continue
		}
		if pos < last {
			return []lint.Finding{finding(lint.Warning, n.RelPath, n.FM.Lines[key], r.fixableSpans(n),
				"frontmatter keys out of template order (%q before %q)", key, order[last])}
		}
		last = pos
	}
	return nil
}

// fixableSpans: the reorder fix only handles the case where every top-level
// key's lines are contiguous single-line scalars or well-nested blocks that
// end before the next top-level key — which is exactly when key line spans
// can be moved verbatim.
func (r keyOrder) fixableSpans(n *vault.Note) bool {
	_, ok := keySpans(n)
	return ok
}

// keySpans maps each top-level key to its [start, end] file-line span.
func keySpans(n *vault.Note) (map[string][2]int, bool) {
	if n.FM == nil || n.FM.Unclosed {
		return nil, false
	}
	starts := make([]int, 0, len(n.FM.Order))
	for _, k := range n.FM.Order {
		line, ok := n.FM.Lines[k]
		if !ok || line == 0 {
			return nil, false
		}
		starts = append(starts, line)
	}
	// Keys must appear in strictly increasing line order for span math.
	for i := 1; i < len(starts); i++ {
		if starts[i] <= starts[i-1] {
			return nil, false
		}
	}
	spans := map[string][2]int{}
	for i, k := range n.FM.Order {
		end := n.FM.EndLine - 1
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		spans[k] = [2]int{starts[i], end}
	}
	return spans, true
}

func (r keyOrder) Fix(ctx *lint.Context, n *vault.Note, f lint.Finding) (*lint.FixResult, error) {
	order, ok := r.orders[typedNote(ctx, n)]
	if !ok {
		return nil, nil
	}
	spans, ok := keySpans(n)
	if !ok {
		return nil, nil
	}
	// Stable re-sort: known keys by template rank; unknown keys keep their
	// relative order and sort after the known prefix they followed.
	keys := append([]string(nil), n.FM.Order...)
	stableSortByRank(keys, order)

	lines := srcLines(n.Src)
	var out [][]byte
	out = append(out, lines[0]) // opening fence
	for _, k := range keys {
		span := spans[k]
		for l := span[0]; l <= span[1] && l <= len(lines); l++ {
			out = append(out, lines[l-1])
		}
	}
	out = append(out, lines[n.FM.EndLine-1:]...)
	newSrc := joinLines(out)
	if len(newSrc) != len(n.Src) {
		return nil, fmt.Errorf("key-order fix would change file size in %s; refusing", n.RelPath)
	}
	if string(newSrc) == string(n.Src) {
		return nil, nil
	}
	return &lint.FixResult{NewSrc: newSrc}, nil
}

func stableSortByRank(keys []string, order []string) {
	// Insertion sort on rank; unknown keys (-1) inherit the rank of the
	// previous known key so they stay attached to it.
	effective := make([]int, len(keys))
	prev := -1
	for i, k := range keys {
		if p := rank(order, k); p >= 0 {
			prev = p
			effective[i] = p * 2
		} else {
			effective[i] = prev*2 + 1
		}
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && effective[j] < effective[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
			effective[j], effective[j-1] = effective[j-1], effective[j]
		}
	}
}
