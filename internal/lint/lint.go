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
	err  error
}

// Fail records the first cannot-evaluate error; the engine aborts the run
// with it. Rules call it when a check cannot be evaluated (a glob or type
// scope doublestar refuses to match) — converting that into "no match"
// would turn "cannot evaluate" into a clean report (fail closed; #30).
func (c *Context) Fail(err error) {
	if c.err == nil {
		c.err = err
	}
}

// Err returns the first recorded cannot-evaluate error.
func (c *Context) Err() error { return c.err }

// TypeOf classifies a note through the profile. A scope glob the profile
// cannot evaluate is recorded on the context and reads as unclassified for
// this call; the engine aborts the run before reporting anything.
func (c *Context) TypeOf(n *vault.Note) string {
	var fields map[string]any
	if n.FM != nil {
		fields = n.FM.Fields
	}
	typ, err := c.Prof.TypeOf(n.RelPath, fields)
	if err != nil {
		c.Fail(err)
		return ""
	}
	return typ
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

var registry = map[string]Factory{}

// Register adds a rule factory; called from init() in the rules package.
func Register(id string, f Factory) {
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
	rules := map[string]any{}
	cfgs := map[string]map[string]any{}
	for _, id := range RuleIDs() {
		if !wanted(id) {
			continue
		}
		cfg := mergeCfg(prof.LintConfig(id), overrides[id])
		if enabled, ok := cfg["enabled"].(bool); ok && !enabled {
			continue
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
		r, err := registry[id](cfg)
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
		// A rule that could not evaluate a check aborts the run: partial
		// findings would read as a complete report (fail closed; #30).
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("rule %s: %w", id, err)
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
	res, err := fixer.Fix(ctx, n, f)
	if err != nil {
		return nil, err
	}
	// A repair computed from a check the rule could not evaluate must not
	// be applied (same posture as Run).
	if cerr := ctx.Err(); cerr != nil {
		return nil, fmt.Errorf("rule %s: %w", f.Rule, cerr)
	}
	return res, nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
