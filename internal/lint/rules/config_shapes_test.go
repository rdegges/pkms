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

// ---- scalar-valued options fail closed (issue #33) -------------------------
//
// These were the KnownGap pins from the #32 gate; the gaps are now closed,
// so they assert the desired behavior.

// A scalar-valued option (`file`, `lists`, `dir`, `key`, `section`, ...)
// written with the wrong type must fail the run — for rules that return nil
// on an empty `file`, dropping it silently DISABLED the check.
func TestWrongTypedScalarOptionFailsTheRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Resources/Personal/Recipes/Recipes.md":     "# Recipes\n",
		"Resources/Personal/Recipes/Uncataloged.md": "---\ntype: recipe\n---\nx\n",
	})
	// Premise: with the profile's own config the rule reports the gap.
	fs, err := lint.Run(ix, prof, nil, []string{"recipes-index-links-complete"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: the uncataloged recipe is a finding: %+v", fs)

	for label, tc := range map[string]struct {
		cfg map[string]any
		key string
	}{
		"file as int":  {map[string]any{"file": 42}, "file"},
		"file as list": {map[string]any{"file": []any{"Resources/Personal/Recipes/Recipes.md"}}, "file"},
		"lists as list": {map[string]any{"file": "Resources/Personal/Recipes/Recipes.md",
			"lists": []any{"Resources/Personal/Recipes/*.md"}}, "lists"},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{"recipes-index-links-complete": tc.cfg},
				[]string{"recipes-index-links-complete"})
			require.Error(t, err, "a wrong-typed scalar option must fail the run")
			require.Contains(t, err.Error(), tc.key, "the error must name the key")
			require.Contains(t, err.Error(), "recipes-index-links-complete",
				"the error must name the rule")
		})
	}
}

// The same wrong-type rejection on every scalar-configured key class, not
// just the one rule the fixture above uses: string keys, int keys, and the
// engine-level `enabled` flag.
func TestWrongTypedScalarOptionFailsEveryScalarConfiguredRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	cases := map[string]struct {
		rule string
		cfg  map[string]any
		key  string
	}{
		"dir as int":        {"no-drafts-folder", map[string]any{"dir": 5}, "dir"},
		"folder as int":     {"attendee-links-resolve-to-people", map[string]any{"folder": 42}, "folder"},
		"section as int":    {"now-active-projects-shape", map[string]any{"file": "Now.md", "section": 42}, "section"},
		"key as int":        {"recipes-count-drift", map[string]any{"file": "R.md", "counts": "R/*.md", "key": 42}, "key"},
		"warn_at as string": {"now-line-cap", map[string]any{"file": "Now.md", "warn_at": "60"}, "warn_at"},
		"warn_at as float":  {"now-line-cap", map[string]any{"file": "Now.md", "warn_at": 60.5}, "warn_at"},
		"min_bullets as bool": {"now-active-projects-shape",
			map[string]any{"file": "Now.md", "min_bullets": true}, "min_bullets"},
		"enabled as string": {"empty-note", map[string]any{"enabled": "false"}, "enabled"},
		"enabled as int":    {"empty-note", map[string]any{"enabled": 1}, "enabled"},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{tc.rule: tc.cfg}, []string{tc.rule})
			require.Errorf(t, err, "%s must reject a wrong-typed %s", tc.rule, tc.key)
			require.Contains(t, err.Error(), tc.key, "the error must name the key")
			require.Contains(t, err.Error(), tc.rule, "the error must name the rule")
		})
	}

	// The valid types keep working: TOML decode delivers int64 for
	// integers, and Go callers pass int; both must be accepted.
	for label, warnAt := range map[string]any{"int64": int64(70), "int": 70} {
		t.Run("valid warn_at "+label, func(t *testing.T) {
			_, err := lint.Run(ix, prof, map[string]map[string]any{
				"now-line-cap": {"file": "Now.md", "warn_at": warnAt},
			}, []string{"now-line-cap"})
			require.NoError(t, err)
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

// The `orders` rewrite is only ever driven from Go maps by the tests above,
// and the shipped profile keeps the rule off — so nothing proved the strict
// reader still accepts what a real profile.toml decodes to. It must enforce
// the order it was given, and its two "unconfigured" exits (no `orders` key,
// empty table) must stay inert rather than become config errors.
func TestKeyOrderReadsARealProfileTOMLOrdersTable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Notes", "A.md"),
		[]byte("---\nmeeting_count: 1\nlast_met: 2026-01-02\n---\nx\n"), 0o644))
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)

	load := func(t *testing.T, lintCfg string) *profile.Profile {
		t.Helper()
		dir := t.TempDir()
		manifest := "schema_version = 1\nname = \"ko\"\nscaffold = [\"Notes\"]\n\n" +
			"[[types]]\nname = \"note\"\nscope = [\"Notes/**\"]\n\n" + lintCfg
		require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
		p, err := profile.Load(dir)
		require.NoError(t, err)
		return p
	}

	configured := load(t, "[lint.frontmatter-key-order]\nenabled = true\n\n"+
		"[lint.frontmatter-key-order.orders]\nnote = [\"last_met\", \"meeting_count\"]\n")
	fs, err := lint.Run(ix, configured, nil, []string{"frontmatter-key-order"})
	require.NoError(t, err, "a TOML orders table must decode to a shape the strict reader accepts")
	require.Len(t, fs, 1, "the configured order must actually be enforced: %+v", fs)
	require.Contains(t, fs[0].Message, "out of template order")

	for label, cfg := range map[string]string{
		"no orders key": "[lint.frontmatter-key-order]\nenabled = true\n",
		"empty orders table": "[lint.frontmatter-key-order]\nenabled = true\n\n" +
			"[lint.frontmatter-key-order.orders]\n",
		"empty per-type list": "[lint.frontmatter-key-order]\nenabled = true\n\n" +
			"[lint.frontmatter-key-order.orders]\nnote = []\n",
	} {
		t.Run(label, func(t *testing.T) {
			fs, err := lint.Run(ix, load(t, cfg), nil, []string{"frontmatter-key-order"})
			require.NoError(t, err, "%s is unconfigured, not malformed", label)
			require.Empty(t, fs, "%s must leave the rule inert: %+v", label, fs)
		})
	}
}

// ---- severity overrides fail closed (issue #34) ----------------------------

// An unrecognized `severity` override must fail the run — dropping it meant
// a user who asked to promote a warning to an error silently kept the
// warning, the same silent-severity failure #29 closed for warning_types.
func TestUnrecognizedSeverityOverrideFailsTheRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Resources/Personal/Orphan.md": "---\ntype: resource\n---\nx\n",
		"index.md":                     "# Index\n",
	})

	// Premise: the exact spellings work, and the promotion is applied.
	fs, err := lint.Run(ix, prof,
		map[string]map[string]any{"orphan-notes": {"severity": "error"}}, []string{"orphan-notes"})
	require.NoError(t, err)
	require.NotEmpty(t, fs, "the fixture must produce an orphan finding")
	require.Positive(t, severities(fs)[lint.Error], "the promotion is applied")
	require.Zero(t, severities(fs)[lint.Warning])
	_, err = lint.Run(ix, prof,
		map[string]map[string]any{"orphan-notes": {"severity": "warning"}}, []string{"orphan-notes"})
	require.NoError(t, err)

	for label, sev := range map[string]any{
		"wrong case": "Error",
		"plural":     "errors",
		"int":        1,
		"bool":       true,
		"empty":      "",
		"list":       []any{"error"},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{"orphan-notes": {"severity": sev}}, []string{"orphan-notes"})
			require.Error(t, err, "an unrecognized severity must fail the run, not be dropped")
			require.Contains(t, err.Error(), "severity", "the error must name the key")
			require.Contains(t, err.Error(), "orphan-notes", "the error must name the rule")
		})
	}
}

// A disabled rule is skipped before its config is read — including its
// severity. Pinned alongside TestDisabledRuleSkipsConfigValidation so the
// enabled-skip stays ahead of the new severity check.
func TestDisabledRuleSkipsSeverityValidation(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	_, err := lint.Run(ix, prof, map[string]map[string]any{
		"orphan-notes": {"enabled": false, "severity": "bogus"},
	}, nil)
	require.NoError(t, err, "disabled rules are skipped before their config is read")
}

// ---- cross-rule config is resolved at construction (issue #35) -------------

// root-file-name-case consumes root-canonical-only's `files` list. That
// read now happens when the rule is CONSTRUCTED — against the merged
// (profile + override) config, with the shape validated — never at check
// time where an error would have to be swallowed. A broken source list
// fails every run that instantiates the reader, including a scoped run and
// a run where the source rule is disabled: config an enabled rule consumes
// must be honorable, whoever owns it.
func TestRootFileNameCaseValidatesTheConfigItConsumes(t *testing.T) {
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

	// A scalar `files` fails a scoped run of the READER rule: the config it
	// consumes is validated when it is built.
	scalar := load(t, "[lint.root-canonical-only]\nfiles = \"Now.md\"\n")
	_, err = lint.Run(ix, scalar, nil, []string{"root-file-name-case"})
	require.Error(t, err, "a scoped run must validate the config the rule consumes")
	require.Contains(t, err.Error(), "root-file-name-case", "the error must name the reader rule")
	require.Contains(t, err.Error(), "files", "the error must name the key")

	// And the full run still fails (both factories reject it).
	_, err = lint.Run(ix, scalar, nil, nil)
	require.Error(t, err)

	// Disabling the SOURCE rule does not skip the reader's validation: the
	// reader is enabled and consumes that list.
	off := load(t, "[lint.root-canonical-only]\nenabled = false\nfiles = \"Now.md\"\n")
	_, err = lint.Run(ix, off, nil, nil)
	require.Error(t, err, "an enabled rule's consumed config must be validated on a full run")
	require.Contains(t, err.Error(), "root-file-name-case")

	// A disabled source with a WELL-SHAPED list still feeds the reader: the
	// case check works even when the canonical-set check is off.
	offGood := load(t, "[lint.root-canonical-only]\nenabled = false\nfiles = [\"Now.md\"]\n")
	fs, err = lint.Run(ix, offGood, nil, nil)
	require.NoError(t, err)
	require.Len(t, fs, 1, "%+v", fs)
	require.Equal(t, "root-file-name-case", fs[0].Rule)
}

// The construction-time read resolves the MERGED config: a vault override
// on root-canonical-only.files must reach root-file-name-case, even on a
// scoped run. (The old check-time read saw the profile table only.)
func TestRootFileNameCaseHonorsOverriddenSourceConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "readme.md"), []byte("x\n"), 0o644))
	dir := t.TempDir()
	manifest := "schema_version = 1\nname = \"ov\"\nscaffold = [\"Areas\"]\n\n" +
		"[[types]]\nname = \"area\"\nscope = [\"Areas/**\"]\n\n" +
		"[lint.root-canonical-only]\nfiles = [\"Now.md\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err)
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)

	// Without the override, "readme.md" is no case variant of "Now.md".
	fs, err := lint.Run(ix, prof, nil, []string{"root-file-name-case"})
	require.NoError(t, err)
	require.Empty(t, fs, "%+v", fs)

	// With an override declaring README.md canonical, the variant is caught.
	over := map[string]map[string]any{"root-canonical-only": {"files": []any{"README.md"}}}
	fs, err = lint.Run(ix, prof, over, []string{"root-file-name-case"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "the reader must see the merged config: %+v", fs)
	require.Equal(t, "root-file-name-case", fs[0].Rule)
}
