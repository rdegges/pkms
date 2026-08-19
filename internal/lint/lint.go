// Package lint is the deterministic rule engine (SPEC §8). Rules are
// registered telegraf-style and parametrized by profile/vault config;
// the engine owns ordering, severity overrides and the fix loop.
package lint

import (
	"fmt"
	"sort"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// Finding is one lint result (stable JSON shape — SPEC §8).
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"severity"`
	Path     string   `json:"path"`
	Line     int      `json:"line,omitempty"`
	Message  string   `json:"message"`
	Fixable  bool     `json:"fixable"`
}

// Context is what rules see.
type Context struct {
	Ix   *vault.Index
	Prof *profile.Profile
}

// TypeOf classifies a note through the profile.
func (c *Context) TypeOf(n *vault.Note) string {
	var fields map[string]any
	if n.FM != nil {
		fields = n.FM.Fields
	}
	return c.Prof.TypeOf(n.RelPath, fields)
}

// NoteRule checks one note at a time.
type NoteRule interface {
	CheckNote(ctx *Context, n *vault.Note) []Finding
}

// VaultRule checks whole-vault invariants (orphans, drift, placement).
type VaultRule interface {
	CheckVault(ctx *Context) []Finding
}

// FixResult is one applied repair: either new file content or a rename.
type FixResult struct {
	NewSrc   []byte
	RenameTo string // vault-relative; empty for content fixes
}

// Fixer is implemented by rules whose findings can be Fixable.
// Fix returns nil (no error) when this particular finding isn't repairable.
type Fixer interface {
	Fix(ctx *Context, n *vault.Note, f Finding) (*FixResult, error)
}

// Factory builds a rule from its merged config. Returning a nil rule (no
// error) means "not applicable with this config" and the rule is skipped.
type Factory func(cfg map[string]any) (any, error)

// PeerFactory builds a rule that also consumes ANOTHER rule's config. The
// peer table is resolved (profile + vault overrides, merged) and consumed
// at construction — never read at check time, where a shape error would
// have to be swallowed (issue #35).
type PeerFactory func(cfg map[string]any, peer func(ruleID string) map[string]any) (any, error)

var registry = map[string]PeerFactory{}

// Register adds a rule factory; called from init() in the rules package.
func Register(id string, f Factory) {
	RegisterPeer(id, func(cfg map[string]any, _ func(string) map[string]any) (any, error) {
		return f(cfg)
	})
}

// RegisterPeer adds a factory that reads another rule's merged config.
func RegisterPeer(id string, f PeerFactory) {
	if _, dup := registry[id]; dup {
		panic("duplicate lint rule id " + id)
	}
	registry[id] = f
}

// RuleIDs returns all registered rule ids, sorted.
func RuleIDs() []string {
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// CfgStrings reads a list-of-strings config value, which may arrive as
// []string (TOML decode) or []any (vault-override merge). An absent key is
// nil. A value that is present but cannot be honored — a bare scalar, or a
// list with a non-string entry — is a config error, never silently dropped:
// dropping it would disable rules and severity downgrades without a word
// (fail closed; the SPEC §31.7 rejectScalarHookCmds precedent).
func CfgStrings(cfg map[string]any, key string) ([]string, error) {
	raw, ok := cfg[key]
	if !ok {
		return nil, nil
	}
	switch xs := raw.(type) {
	case []string:
		return xs, nil
	case []any:
		out := make([]string, 0, len(xs))
		for i, e := range xs {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: got %T (%v), want string", key, i, e, e)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: got %T, want a list of strings", key, raw)
}

// CfgString reads a string config value. An absent key is the default; a
// present value of any other type is a config error, never silently
// dropped — for rules that turn off on an empty string, dropping it
// disabled the check without a word (fail closed; issue #33).
func CfgString(cfg map[string]any, key, def string) (string, error) {
	raw, ok := cfg[key]
	if !ok {
		return def, nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s: got %T (%v), want a string", key, raw, raw)
	}
	return s, nil
}

// CfgInt reads an integer config value. TOML decode delivers int64 and Go
// callers pass int; anything else — including a float, which would
// silently truncate — is a config error (issue #33).
func CfgInt(cfg map[string]any, key string, def int) (int, error) {
	raw, ok := cfg[key]
	if !ok {
		return def, nil
	}
	switch v := raw.(type) {
	case int64:
		return int(v), nil
	case int:
		return v, nil
	}
	return 0, fmt.Errorf("%s: got %T (%v), want an integer", key, raw, raw)
}

// CfgBool reads a boolean config value; a non-bool is a config error
// (issue #33).
func CfgBool(cfg map[string]any, key string, def bool) (bool, error) {
	raw, ok := cfg[key]
	if !ok {
		return def, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s: got %T (%v), want a boolean", key, raw, raw)
	}
	return b, nil
}

// mergeCfg overlays vault-level overrides on the profile's rule config.
func mergeCfg(base, over map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		out[k] = v
	}
	return out
}

// instantiate builds the enabled rule set for a run.
func instantiate(prof *profile.Profile, overrides map[string]map[string]any, only []string) (map[string]any, map[string]map[string]any, error) {
	// A typo'd rule id must not masquerade as a clean run.
	for _, o := range only {
		if _, ok := registry[o]; !ok {
			return nil, nil, fmt.Errorf("unknown lint rule %q (see `pkms lint --help` or docs/LINT-RULES.md)", o)
		}
	}
	wanted := func(id string) bool {
		if len(only) == 0 {
			return true
		}
		for _, o := range only {
			if o == id {
				return true
			}
		}
		return false
	}
	peer := func(pid string) map[string]any {
		return mergeCfg(prof.LintConfig(pid), overrides[pid])
	}
	rules := map[string]any{}
	cfgs := map[string]map[string]any{}
	for _, id := range RuleIDs() {
		if !wanted(id) {
			continue
		}
		cfg := mergeCfg(prof.LintConfig(id), overrides[id])
		// enabled must be a bool — a wrong-typed value must not silently
		// leave the rule on (fail closed; issue #33). The skip stays ahead
		// of all other validation: a disabled rule's config is not read.
		enabled, err := CfgBool(cfg, "enabled", true)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %s: %w", id, err)
		}
		if !enabled {
			continue
		}
		// severity must be exactly "error" or "warning" — an unrecognized
		// spelling must not silently keep the profile's severity (fail
		// closed; issue #34).
		if raw, ok := cfg["severity"]; ok {
			s, isStr := raw.(string)
			if !isStr || (s != string(Error) && s != string(Warning)) {
				return nil, nil, fmt.Errorf(`rule %s: severity: got %v (%T), want "error" or "warning"`, id, raw, raw)
			}
		}
		// warning_types must name declared profile types — a typo'd type
		// name or a wrong-shaped value must not silently leave severities
		// unchanged (fail closed).
		names, err := CfgStrings(cfg, "warning_types")
		if err != nil {
			return nil, nil, fmt.Errorf("rule %s: %w", id, err)
		}
		for _, name := range names {
			if prof.Type(name) == nil {
				return nil, nil, fmt.Errorf("rule %s: warning_types names unknown note type %q (profile %q declares no such type)", id, name, prof.Name)
			}
		}
		r, err := registry[id](cfg, peer)
		if err != nil {
			return nil, nil, fmt.Errorf("rule %s: %w", id, err)
		}
		if r == nil {
			continue
		}
		rules[id] = r
		cfgs[id] = cfg
	}
	return rules, cfgs, nil
}

// Run executes all enabled rules and returns deterministically sorted
// findings. overrides come from per-vault config; only limits rule ids.
func Run(ix *vault.Index, prof *profile.Profile, overrides map[string]map[string]any, only []string) ([]Finding, error) {
	ctx := &Context{Ix: ix, Prof: prof}
	rules, cfgs, err := instantiate(prof, overrides, only)
	if err != nil {
		return nil, err
	}

	var findings []Finding
	collect := func(id string, fs []Finding) {
		sevOverride, _ := cfgs[id]["severity"].(string)
		for _, f := range fs {
			f.Rule = id
			if sevOverride == string(Warning) || sevOverride == string(Error) {
				f.Severity = Severity(sevOverride)
			}
			findings = append(findings, f)
		}
	}

	for _, id := range sortedKeys(rules) {
		r := rules[id]
		if nr, ok := r.(NoteRule); ok {
			for _, p := range ix.NotePaths() {
				collect(id, nr.CheckNote(ctx, ix.Notes[p]))
			}
		}
		if vr, ok := r.(VaultRule); ok {
			collect(id, vr.CheckVault(ctx))
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Rule != b.Rule {
			return a.Rule < b.Rule
		}
		return a.Message < b.Message
	})
	return findings, nil
}

// Fix computes the repair for one fixable finding. It instantiates the rule
// through the same validation path as Run — a config Run would reject must
// be rejected here too, or --fix becomes a validation bypass.
func Fix(ix *vault.Index, prof *profile.Profile, overrides map[string]map[string]any, f Finding) (*FixResult, error) {
	ctx := &Context{Ix: ix, Prof: prof}
	rules, _, err := instantiate(prof, overrides, []string{f.Rule})
	if err != nil {
		return nil, err
	}
	r, ok := rules[f.Rule]
	if !ok {
		return nil, nil // disabled or not applicable with this config
	}
	fixer, ok := r.(Fixer)
	if !ok {
		return nil, nil
	}
	n := ix.Notes[f.Path]
	if n == nil {
		return nil, nil
	}
	return fixer.Fix(ctx, n, f)
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
