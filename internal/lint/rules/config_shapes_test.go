package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// ---- CfgStrings: every non-list shape a config can hold -------------------

// The #31 fix routes every list-valued option through lint.CfgStrings. Its
// contract is "absent is nil, anything unusable is an error" — so every shape
// that is neither []string nor []any must fail the run, not default to empty.
// A shape that slipped through would disable the rule in silence.
func TestNonListConfigValueFailsTheRunForEveryShape(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for label, v := range map[string]any{
		"nil":          nil,
		"bool":         true,
		"int":          42,
		"int64":        int64(42),
		"float":        1.5,
		"string":       "Areas/**",
		"empty string": "",
		"map":          map[string]any{"Areas": "**"},
		"list of any":  []any{[]any{"Areas/**"}},
		"list of int":  []any{1, 2},
		"list of nil":  []any{nil},
		"list of map":  []any{map[string]any{"a": 1}},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": v}},
				[]string{"non-markdown-in-note-folders"})
			require.Errorf(t, err, "scopes as %s must fail the run", label)
			require.Contains(t, err.Error(), "scopes", "the error must name the key")
			require.Contains(t, err.Error(), "non-markdown-in-note-folders",
				"the error must name the rule")
		})
	}
}

// An empty list is a shape the engine CAN honor, so it is not an error — but
// what it then means differs per rule, and that difference is load-bearing.
// Pinned so it is a decision, not an accident.
func TestEmptyListIsHonoredNotRejected(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
		"Areas/Personal/lost.bak": "x\n",
	})

	// scopes = [] -> the rule is not applicable and reports nothing.
	fs, err := lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{}}},
		[]string{"non-markdown-in-note-folders"})
	require.NoError(t, err, "an empty list is a usable shape, not a config error")
	require.Empty(t, fs, "%+v", fs)

	// patterns = [] -> no-junk-files falls back to its shipped defaults, so
	// the .bak file is still caught. Empty means "unset", not "match nothing".
	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"no-junk-files": {"patterns": []any{}}},
		[]string{"no-junk-files"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "the shipped default patterns still apply: %+v", fs)
	require.Equal(t, "Areas/Personal/lost.bak", fs[0].Path)
}

// The key must be absent, not empty, for "unconfigured" to mean unconfigured:
// `severity`-only overrides (what the live vault ships) must stay unaffected
// by the new validation on every list-valued rule.
func TestSeverityOnlyOverrideValidatesCleanOnEveryRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, id := range lint.RuleIDs() {
		for _, sev := range []string{"error", "warning"} {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{id: {"severity": sev}}, []string{id})
			require.NoErrorf(t, err, "rule %q with a severity-only override", id)
		}
	}
}

// ---- lint.Fix: the rewritten instantiate path -----------------------------

// Fix now builds its rule through instantiate, which honors `enabled = false`
// — so a disabled rule is inert on the fix path. Pinned because it is a
// behavior change: the old Fix ignored `enabled` and would still have
// repaired the finding.
func TestFixIsInertForADisabledRule(t *testing.T) {
	ix, prof, f := fixableTopicsFinding(t)

	res, err := lint.Fix(ix, prof, nil, f)
	require.NoError(t, err)
	require.NotNil(t, res, "premise: this finding is repairable with the default config")

	res, err = lint.Fix(ix, prof,
		map[string]map[string]any{"person-topics-kebab-case": {"enabled": false}}, f)
	require.NoError(t, err, "a disabled rule is skipped, not an error")
	require.Nil(t, res, "a disabled rule must not repair anything")
}

// A typo'd rule id on the fix path must stop, not silently no-op.
func TestFixRejectsAnUnknownRuleID(t *testing.T) {
	ix, prof, f := fixableTopicsFinding(t)
	f.Rule = "person-topics-kebab-cases"
	_, err := lint.Fix(ix, prof, nil, f)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown lint rule")
}

// Validation on the fix path must not cost the repair: a rule whose config is
// fine still produces its fix after the rewrite.
func TestFixStillRepairsWithValidConfig(t *testing.T) {
	ix, prof, f := fixableTopicsFinding(t)
	res, err := lint.Fix(ix, prof,
		map[string]map[string]any{"person-topics-kebab-case": {"severity": "warning"}}, f)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Contains(t, string(res.NewSrc), "ai-security")
}

// The two remaining "nothing to do" exits of the rewritten Fix. Both must be
// silent no-ops rather than errors — the fix loop treats a nil result as
// "not applicable right now" and keeps going.
func TestFixNoOpsForNonFixableRulesAndUnknownPaths(t *testing.T) {
	ix, prof, f := fixableTopicsFinding(t)

	notAFixer := f
	notAFixer.Rule = "orphan-notes"
	res, err := lint.Fix(ix, prof,
		map[string]map[string]any{"orphan-notes": {"scopes": []any{"People/**"}}}, notAFixer)
	require.NoError(t, err, "a rule with no Fix method is a no-op, not an error")
	require.Nil(t, res)

	missing := f
	missing.Path = "People/Snyk/Gone.md"
	res, err = lint.Fix(ix, prof, nil, missing)
	require.NoError(t, err, "a finding whose note left the index is a no-op")
	require.Nil(t, res)
}

// fixableTopicsFinding returns a real finding produced by the engine, so the
// fix path sees the message it parses rather than a hand-built stand-in.
func fixableTopicsFinding(t *testing.T) (*vault.Index, *profile.Profile, lint.Finding) {
	t.Helper()
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"People/Snyk/Fixme.md": "---\nlast_met: 2026-01-02\nmeeting_count: 1\n" +
			"topics:\n  - AI Security\n---\nbody\n",
	})
	fs, err := lint.Run(ix, prof, nil, []string{"person-topics-kebab-case"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: the fixture produces exactly one finding: %+v", fs)
	require.True(t, fs[0].Fixable)
	return ix, prof, fs[0]
}

// ---- known fail-open gaps this change did not close -----------------------
//
// These pin CURRENT behavior, not desired behavior. Each is the same class of
// silent-config failure #30/#31 closed for globs and lists, one config shape
// away. If a later change closes one, the test below fails — that failure is
// the fix landing, and the test should be inverted, not deleted.

// GAP: only LIST-valued options fail closed. A scalar-valued option
// (`file`, `lists`, `dir`, `key`, `section`) written with the wrong type is
// still dropped by cfgString, and for rules that return nil on an empty
// `file` that silently DISABLES the check.
func TestKnownGap_WrongTypedScalarOptionSilentlyDisablesARule(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Resources/Personal/Recipes/Recipes.md":     "# Recipes\n",
		"Resources/Personal/Recipes/Uncataloged.md": "---\ntype: recipe\n---\nx\n",
	})
	// Premise: with the profile's own config the rule reports the gap.
	fs, err := lint.Run(ix, prof, nil, []string{"recipes-index-links-complete"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: the uncataloged recipe is a finding: %+v", fs)

	for label, cfg := range map[string]map[string]any{
		"file as int":  {"file": 42},
		"file as list": {"file": []any{"Resources/Personal/Recipes/Recipes.md"}},
		"lists as list": {"file": "Resources/Personal/Recipes/Recipes.md",
			"lists": []any{"Resources/Personal/Recipes/*.md"}},
	} {
		t.Run(label, func(t *testing.T) {
			fs, err := lint.Run(ix, prof,
				map[string]map[string]any{"recipes-index-links-complete": cfg},
				[]string{"recipes-index-links-complete"})
			require.NoError(t, err, "GAP: a wrong-typed scalar option does not fail the run")
			require.Empty(t, fs, "GAP: the rule is silently disabled instead: %+v", fs)
		})
	}
}

// frontmatter-key-order was missed by #31's first sweep. Its `orders` table
// is config too: a wrong-shaped `orders`, a wrong-shaped per-type list
// inside it, or a non-string entry must fail the run like every other
// list-valued option — never silently disable the check or enforce an
// order the user did not write.
func TestKeyOrderConfigShapesFailTheRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"People/Snyk/Jane.md": "---\nmeeting_count: 1\nlast_met: 2026-01-02\n" +
			"topics:\n  - ai\n---\nx\n",
	})
	run := func(t *testing.T, orders any) ([]lint.Finding, error) {
		t.Helper()
		return lint.Run(ix, prof, map[string]map[string]any{
			"frontmatter-key-order": {"enabled": true, "orders": orders},
		}, []string{"frontmatter-key-order"})
	}

	// Premise: with a well-shaped table the rule reports the out-of-order key.
	fs, err := run(t, map[string]any{
		"person": []any{"last_met", "meeting_count", "topics"},
	})
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: the fixture is out of order: %+v", fs)

	for label, orders := range map[string]any{
		"orders as a list":       []any{"last_met"},
		"orders as a string":     "last_met",
		"per-type as a string":   map[string]any{"person": "last_met"},
		"per-type as an int":     map[string]any{"person": 42},
		"per-type list of lists": map[string]any{"person": []any{[]any{"last_met"}}},
		"non-string entry":       map[string]any{"person": []any{"last_met", 42, "topics"}},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := run(t, orders)
			require.Error(t, err, "a wrong-shaped orders value must fail the run")
			require.Contains(t, err.Error(), "frontmatter-key-order",
				"the error must name the rule")
			require.Contains(t, err.Error(), "orders", "the error must name the key")
		})
	}
}

// GAP: an unrecognized `severity` override is dropped without a word, so a
// user who asks to promote a warning to an error silently keeps the warning.
// This is the same silent-severity failure #29 closed for warning_types.
func TestKnownGap_UnrecognizedSeverityOverrideIsSilentlyIgnored(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Resources/Personal/Orphan.md": "---\ntype: resource\n---\nx\n",
		"index.md":                     "# Index\n",
	})
	sevCount := func(sev any) (int, int) {
		fs, err := lint.Run(ix, prof,
			map[string]map[string]any{"orphan-notes": {"severity": sev}}, []string{"orphan-notes"})
		require.NoError(t, err)
		require.NotEmpty(t, fs, "the fixture must produce an orphan finding")
		s := severities(fs)
		return s[lint.Error], s[lint.Warning]
	}
	errs, warns := sevCount("error")
	require.Positive(t, errs, "premise: the exact spelling promotes the finding")
	require.Zero(t, warns)

	for label, sev := range map[string]any{
		"wrong case": "Error",
		"plural":     "errors",
		"int":        1,
		"bool":       true,
	} {
		t.Run(label, func(t *testing.T) {
			errs, warns := sevCount(sev)
			require.Zero(t, errs, "GAP: the promotion the user asked for was dropped")
			require.Positive(t, warns, "GAP: it silently stays a warning")
		})
	}
}

// GAP: root-file-name-case reads root-canonical-only's `files` at CHECK time
// and swallows the shape error, so a broken (or merely disabled) source rule
// silently turns root-file-name-case into a no-op. Unlike the scoped-run case
// the maker documented, `enabled = false` plus a bad shape reaches this on a
// FULL run — nothing validates the config and nothing reports the miss.
func TestKnownGap_RootFileNameCaseGoesQuietWhenItsSourceRuleIsBroken(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "now.md"), []byte("x\n"), 0o644))

	load := func(t *testing.T, canonicalCfg string) *profile.Profile {
		t.Helper()
		dir := t.TempDir()
		manifest := "schema_version = 1\nname = \"gap\"\nscaffold = [\"Areas\"]\n\n" +
			"[[types]]\nname = \"area\"\nscope = [\"Areas/**\"]\n\n" + canonicalCfg
		require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
		p, err := profile.Load(dir)
		require.NoError(t, err)
		return p
	}
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)

	// Premise: with a well-shaped list the rule catches the miscased root file.
	good := load(t, "[lint.root-canonical-only]\nfiles = [\"Now.md\"]\n")
	fs, err := lint.Run(ix, good, nil, nil)
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: 'now.md' is a case variant of 'Now.md': %+v", fs)
	require.Equal(t, "root-file-name-case", fs[0].Rule)

	// A scalar `files` fails the full run (root-canonical-only's own factory
	// rejects it) but NOT a scoped run of the reader rule.
	scalar := load(t, "[lint.root-canonical-only]\nfiles = \"Now.md\"\n")
	_, err = lint.Run(ix, scalar, nil, nil)
	require.Error(t, err, "the source rule's own factory catches it on a full run")
	fs, err = lint.Run(ix, scalar, nil, []string{"root-file-name-case"})
	require.NoError(t, err, "GAP: a scoped run never validates the config it reads")
	require.Empty(t, fs, "GAP: root-file-name-case silently checks nothing: %+v", fs)

	// Disabled + scalar is the reachable one: nothing validates it on ANY run
	// and root-file-name-case reports clean.
	off := load(t, "[lint.root-canonical-only]\nenabled = false\nfiles = \"Now.md\"\n")
	fs, err = lint.Run(ix, off, nil, nil)
	require.NoError(t, err, "GAP: a disabled rule's broken config is never validated")
	require.Empty(t, fs, "GAP: a full run reports clean and the check is gone: %+v", fs)
}
