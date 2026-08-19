package rules_test

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// buildVaultWith indexes a synthetic vault against an arbitrary profile
// (buildVault is pinned to rdegges).
func buildVaultWith(tb testing.TB, profName string, files map[string]string) (*vault.Index, *profile.Profile) {
	tb.Helper()
	root := tb.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(tb, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(tb, os.WriteFile(p, []byte(content), 0o644))
	}
	prof, err := profile.Load(profName)
	require.NoError(tb, err)
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(tb, err)
	return ix, prof
}

// severities returns the distinct severities across findings.
func severities(fs []lint.Finding) map[lint.Severity]int {
	out := map[lint.Severity]int{}
	for _, f := range fs {
		out[f.Severity]++
	}
	return out
}

// ---- no-junk-files: malformed glob patterns -----------------------------

// Every shape path.Match refuses must be refused at construction time, and
// the message must name the offending pattern so the user can find it in
// their config.
func TestJunkPatternRejectsEveryMalformedGlobShape(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	malformed := []string{
		"[unclosed",  // unterminated class
		"[a-",        // unterminated range
		"[^",         // unterminated negation (caret form)
		"[!",         // unterminated negation (bang form)
		"[]",         // empty class
		"a[]b",       // empty class mid-pattern
		"x*[",        // bad class after a star (mismatch happens first)
		"*[",         // bad class with a leading star
		`abc\`,       // trailing escape
		"*/[",        // bad class after a separator the base name can't have
		"* conf[ict", // realistic typo in the shipped default pattern
	}
	for _, pat := range malformed {
		t.Run(pat, func(t *testing.T) {
			// Guard the premise: these really are patterns path.Match rejects.
			_, perr := path.Match(pat, "probe")
			require.Error(t, perr, "premise: path.Match must reject %q", pat)

			over := map[string]map[string]any{"no-junk-files": {"patterns": []any{pat}}}
			_, err := lint.Run(ix, prof, over, []string{"no-junk-files"})
			require.Error(t, err, "malformed pattern %q must fail the run, not match nothing", pat)
			require.Contains(t, err.Error(), pat, "the error must name the offending pattern")
			require.Contains(t, err.Error(), "no-junk-files", "the error must name the rule")
		})
	}
}

// The valid forms — including every shipped default and non-ASCII names —
// must keep working; a fail-closed check that rejects good config is worse
// than the bug it fixes.
func TestJunkPatternAcceptsValidGlobs(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	valid := [][]string{
		{"*.bak", ".DS_Store", "* conflicted copy*", "~$*", "*.tmp"}, // the shipped defaults
		{"*"},
		{"**"},
		{"[abc]*"},
		{"[a-b].md"},
		{"[!x]*"},
		{`\[literal\].md`},
		{"café*.md"},     // non-ASCII
		{"конфликт*.md"}, // non-ASCII, non-Latin
		{"*.bak", "*.tmp", "*.orig"},
	}
	for _, pats := range valid {
		anys := make([]any, len(pats))
		for i, p := range pats {
			anys[i] = p
		}
		over := map[string]map[string]any{"no-junk-files": {"patterns": anys}}
		_, err := lint.Run(ix, prof, over, []string{"no-junk-files"})
		require.NoError(t, err, "valid patterns %v must not be rejected", pats)
	}
}

// Overrides reach the engine as []any from TOML but as []string from Go
// callers; both shapes must be validated, or the check has a bypass.
func TestJunkPatternValidatesBothConfigSliceShapes(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for name, patterns := range map[string]any{
		"[]any":    []any{"[unclosed"},
		"[]string": []string{"[unclosed"},
	} {
		over := map[string]map[string]any{"no-junk-files": {"patterns": patterns}}
		_, err := lint.Run(ix, prof, over, []string{"no-junk-files"})
		require.Error(t, err, "%s config shape must be validated too", name)
	}
}

// A malformed pattern must fail the run even when the rule would report
// nothing anyway: an empty vault is the "nothing to do" branch, and a gate
// that goes green because it had nothing to check proves nothing.
func TestJunkPatternFailsClosedOnAnEmptyVault(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{})
	over := map[string]map[string]any{"no-junk-files": {"patterns": []any{"[unclosed"}}}
	_, err := lint.Run(ix, prof, over, []string{"no-junk-files"})
	require.Error(t, err)
}

// The invariant behind the probe-string check: any pattern the factory
// accepts must be safe to feed to path.Match at match time, for every base
// name. If this ever finds a divergence, that pattern silently matches
// nothing in production — the exact bug #26 fixed.
func FuzzJunkPatternAcceptedImpliesMatchable(f *testing.F) {
	for _, seed := range []string{
		"*.bak", "[unclosed", "[a-", "x*[", `abc\`, "[]", "*", "**", "?", "[a-b]",
		"[!]", "a\\", "\\", "[[]", "café*", "\x00", "*/*", "[--]", "[^a]",
	} {
		f.Add(seed)
	}
	ix, prof := buildVaultWith(f, "rdegges", map[string]string{
		"Areas/Personal/n.md": "x\n",
	})
	names := []string{"", "probe", "n.md", "a.bak", ".DS_Store", "~$doc.tmp",
		"café.md", "a/b", "x", "Some Long Name — with unicode.md", "\x00"}

	f.Fuzz(func(t *testing.T, pattern string) {
		over := map[string]map[string]any{"no-junk-files": {"patterns": []any{pattern}}}
		_, runErr := lint.Run(ix, prof, over, []string{"no-junk-files"})
		if runErr != nil {
			return // rejected up front: fail-closed, nothing to prove
		}
		for _, n := range names {
			if _, err := path.Match(pattern, n); err != nil {
				t.Fatalf("pattern %q was accepted but path.Match(%q, %q) fails: %v — "+
					"it would silently match nothing at run time", pattern, pattern, n, err)
			}
		}
	})
}

// ---- warning_types: unknown profile type names --------------------------

// Every rule that reads warning_types must reject names the profile does
// not declare, in every wrong-name shape a human actually produces.
func TestUnknownWarningTypeRejectedForEveryRuleAndShape(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	rules := []string{"frontmatter-schema", "frontmatter-present"}
	bad := map[string]string{
		"plural":         "meetings",
		"wrong case":     "Meeting",
		"leading space":  " meeting",
		"trailing space": "meeting ",
		"underscored":    "session_trace",
		"empty":          "",
		"non-ascii":      "réunion",
		"path-like":      "Meetings/Snyk",
	}
	for _, rule := range rules {
		for label, name := range bad {
			t.Run(rule+"/"+label, func(t *testing.T) {
				over := map[string]map[string]any{rule: {"warning_types": []any{name}}}
				_, err := lint.Run(ix, prof, over, []string{rule})
				require.Error(t, err, "%s must reject %q", rule, name)
				require.Contains(t, err.Error(), rule, "the error must name the rule")
				require.Contains(t, err.Error(), prof.Name, "the error must name the profile")
			})
		}
	}
}

// One bad name in an otherwise valid list must still fail the run —
// validation cannot stop at the first entry it likes.
func TestUnknownWarningTypeRejectedAnywhereInTheList(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, list := range [][]any{
		{"meetings", "person"},           // bad first
		{"person", "meetings"},           // bad last
		{"person", "meetings", "person"}, // bad in the middle
	} {
		over := map[string]map[string]any{"frontmatter-schema": {"warning_types": list}}
		_, err := lint.Run(ix, prof, over, []string{"frontmatter-schema"})
		require.Error(t, err, "list %v must be rejected", list)
		require.Contains(t, err.Error(), `"meetings"`)
	}
}

// Both config slice shapes must be validated (see the junk-pattern twin).
func TestWarningTypesValidatesBothConfigSliceShapes(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for name, list := range map[string]any{
		"[]any":    []any{"meetings"},
		"[]string": []string{"meetings"},
	} {
		over := map[string]map[string]any{"frontmatter-schema": {"warning_types": list}}
		_, err := lint.Run(ix, prof, over, []string{"frontmatter-schema"})
		require.Error(t, err, "%s config shape must be validated too", name)
	}
}

// Every type the profile declares must be accepted, and the severity it
// buys must actually be applied — a validation that passed but silently
// dropped the list would look identical without this second half.
func TestDeclaredWarningTypesAreAcceptedAndHonored(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, typ := range prof.Types {
		over := map[string]map[string]any{"frontmatter-schema": {"warning_types": []any{typ.Name}}}
		_, err := lint.Run(ix, prof, over, []string{"frontmatter-schema"})
		require.NoErrorf(t, err, "declared type %q must be accepted", typ.Name)
	}

	// Severity consequence: with "person" listed the broken person note is a
	// warning; with only "meeting" listed the same note is an error.
	files := map[string]string{
		"People/Snyk/Broken.md": "---\nname: Jane\n---\nx\n",
	}
	ixp, profp := buildVaultWith(t, "rdegges", files)
	sevFor := func(list []any) map[lint.Severity]int {
		over := map[string]map[string]any{"frontmatter-schema": {"warning_types": list}}
		fs, err := lint.Run(ixp, profp, over, []string{"frontmatter-schema"})
		require.NoError(t, err)
		require.NotEmpty(t, fs, "the fixture must produce a schema finding")
		return severities(fs)
	}
	require.Zero(t, sevFor([]any{"person"})[lint.Error], "person listed → warnings only")
	require.Positive(t, sevFor([]any{"person"})[lint.Warning])
	require.Zero(t, sevFor([]any{"meeting"})[lint.Warning], "person not listed → errors only")
	require.Positive(t, sevFor([]any{"meeting"})[lint.Error])
}

// ---- the shipped profiles must survive their own validation -------------

// The new checks run against the merged config, which includes the
// profile's own [lint.*] tables — so a typo in a shipped profile now bricks
// every lint run for every user of it. This is the guard for that.
func TestEveryBuiltinProfileInstantiatesEveryRuleCleanly(t *testing.T) {
	for _, name := range profile.Builtins() {
		t.Run(name, func(t *testing.T) {
			ix, prof := buildVaultWith(t, name, map[string]string{})
			_, err := lint.Run(ix, prof, nil, nil)
			require.NoError(t, err, "profile %q must instantiate every rule", name)

			// Per-rule too: `--rules <id>` is a different instantiate path
			// (only-filtered), and a whole-run pass can hide a rule that is
			// skipped for being disabled in the merged config.
			for _, id := range lint.RuleIDs() {
				_, err := lint.Run(ix, prof, nil, []string{id})
				require.NoErrorf(t, err, "profile %q rule %q", name, id)
			}
		})
	}
}

// Direct restatement of the same guard at the source: every name a shipped
// profile lists in warning_types resolves to a declared type.
func TestBuiltinProfileWarningTypesNameDeclaredTypes(t *testing.T) {
	for _, name := range profile.Builtins() {
		prof, err := profile.Load(name)
		require.NoError(t, err)
		for _, id := range lint.RuleIDs() {
			cfg := prof.LintConfig(id)
			raw, ok := cfg["warning_types"]
			if !ok {
				continue
			}
			list, ok := raw.([]any)
			require.Truef(t, ok, "profile %q rule %q: warning_types decoded as %T, "+
				"not []any — the engine's validator only understands []any/[]string", name, id, raw)
			for _, e := range list {
				s, ok := e.(string)
				require.Truef(t, ok, "profile %q rule %q: non-string warning_types entry %v", name, id, e)
				require.NotNilf(t, prof.Type(s), "profile %q rule %q lists undeclared type %q", name, id, s)
			}
		}
	}
}

// ---- the scope of the gate ----------------------------------------------

// Config validation happens while instantiating the rules a run selects, so
// `--rules` narrows what gets validated. Pinned deliberately: a user who
// lints one rule does not get told about a typo in another rule's config.
func TestConfigValidationIsScopedToTheSelectedRules(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	over := map[string]map[string]any{"frontmatter-schema": {"warning_types": []any{"meetings"}}}

	_, err := lint.Run(ix, prof, over, []string{"empty-note"})
	require.NoError(t, err, "an unselected rule's broken config is not validated")

	_, err = lint.Run(ix, prof, over, nil)
	require.Error(t, err, "a full run must still catch it")
}

// A rule turned off in config is not instantiated, so its config is not
// validated. Pinned so the behavior is a decision, not an accident.
func TestDisabledRuleSkipsConfigValidation(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	over := map[string]map[string]any{
		"no-junk-files": {"enabled": false, "patterns": []any{"[unclosed"}},
	}
	_, err := lint.Run(ix, prof, over, nil)
	require.NoError(t, err, "disabled rules are skipped before their config is read")
}

// instantiate's other fail-closed check, previously untested: a rule id
// that does not exist must stop the run rather than silently narrow it to
// nothing — an empty selection is the "nothing to do" green nobody notices.
func TestUnknownRuleIDFailsTheRun(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, id := range []string{"no-junk-file", "", "No-Junk-Files", "empty note"} {
		_, err := lint.Run(ix, prof, nil, []string{id})
		require.Errorf(t, err, "rule id %q must be rejected", id)
		require.Contains(t, err.Error(), "unknown lint rule")
	}
	// A valid id alongside a bad one still fails: no partial runs.
	_, err := lint.Run(ix, prof, nil, []string{"empty-note", "no-junk-file"})
	require.Error(t, err)
}

// The regex-configured rules were already fail-closed; pin it so the class
// stays consistent as rules are added.
func TestRegexConfiguredRulesAlsoFailClosed(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, rule := range []string{"no-per-run-notes", "no-drafts-folder"} {
		over := map[string]map[string]any{rule: {"patterns": []any{"("}}}
		_, err := lint.Run(ix, prof, over, []string{rule})
		require.Error(t, err, "%s must reject an uncompilable regex", rule)
		require.Contains(t, err.Error(), rule)
	}
}

// ---- scope globs fail closed (issue #30) ---------------------------------
//
// These were the KnownGap pins from the #29 gate; the gap is now closed, so
// they assert the desired behavior: a malformed doublestar glob must fail
// the run at construction time, never silently match nothing.

// A glob rule other than no-junk-files must reject malformed patterns too.
func TestMalformedScopeGlobFailsTheRun(t *testing.T) {
	require.False(t, doublestar.ValidatePattern("[unclosed"),
		"premise: doublestar rejects the pattern")

	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
	})
	over := map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{"[unclosed"}},
	}
	_, err := lint.Run(ix, prof, over, []string{"non-markdown-in-note-folders"})
	require.Error(t, err, "a malformed scope glob must fail the run")
	require.Contains(t, err.Error(), "[unclosed", "the error must name the offending pattern")
	require.Contains(t, err.Error(), "non-markdown-in-note-folders", "the error must name the rule")

	// The same rule with a valid scope does find the file — proving the
	// validation did not simply disable the rule.
	fs, err := lint.Run(ix, prof, map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{"Areas/**"}},
	}, []string{"non-markdown-in-note-folders"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "%+v", fs)
}

// Every other config key holding doublestar globs gets the same treatment:
// orphan-notes scopes, the index rules' lists globs, and the count-drift
// counts glob.
func TestMalformedGlobFailsEveryGlobConfiguredRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	cases := map[string]map[string]any{
		"orphan-notes":                 {"scopes": []any{"[unclosed"}},
		"resources-cataloged-in-index": {"file": "index.md", "lists": "[unclosed"},
		"projects-linked-from-master":  {"file": "Projects.md", "lists": "[unclosed"},
		"recipes-index-links-complete": {"file": "Recipes.md", "lists": "[unclosed"},
		"recipes-count-drift":          {"file": "Recipes.md", "counts": "[unclosed"},
	}
	for rule, cfg := range cases {
		t.Run(rule, func(t *testing.T) {
			over := map[string]map[string]any{rule: cfg}
			_, err := lint.Run(ix, prof, over, []string{rule})
			require.Error(t, err, "%s must reject a malformed glob", rule)
			require.Contains(t, err.Error(), "[unclosed", "the error must name the offending pattern")
			require.Contains(t, err.Error(), rule, "the error must name the rule")
		})
	}
}

// ---- list-shaped config fails closed (issue #31) --------------------------

// A list-valued option written as a bare string must fail the run — for
// rules whose factory returns nil on an empty list, dropping it silently
// DISABLED the rule (config that cannot be honored turned a check off).
func TestScalarInsteadOfListFailsTheRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
	})
	over := map[string]map[string]any{
		// The intent is obvious; the shape is wrong (string, not list).
		"non-markdown-in-note-folders": {"scopes": "Areas/**"},
	}
	_, err := lint.Run(ix, prof, over, []string{"non-markdown-in-note-folders"})
	require.Error(t, err, "a wrong-shaped value must fail the run, not disable the rule")
	require.Contains(t, err.Error(), "scopes", "the error must name the key")
	require.Contains(t, err.Error(), "non-markdown-in-note-folders", "the error must name the rule")
}

// The same scalar-for-list shape must be rejected on every list-valued key,
// not just the one the fixture above happens to use.
func TestScalarInsteadOfListFailsEveryListConfiguredRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	cases := map[string]map[string]any{
		"no-junk-files":                {"patterns": "*.bak"},
		"orphan-notes":                 {"scopes": "Resources/**"},
		"person-required-sections":     {"sections": "About"},
		"project-status-vocab":         {"allowlist": "active"},
		"index-no-inventory":           {"file": "index.md", "forbidden_prefixes": "- ["},
		"related-projects-resolve":     {"folders": "Projects/"},
		"date-format-iso":              {"keys": "date"},
		"root-canonical-only":          {"files": "Now.md"},
		"top-level-folders-fixed":      {"dirs": "Projects"},
		"no-per-run-notes":             {"patterns": "^run-"},
		"frontmatter-present":          {"warning_types": "person"},
		"non-markdown-in-note-folders": {"scopes": 42}, // wrong scalar type too
	}
	for rule, cfg := range cases {
		t.Run(rule, func(t *testing.T) {
			var key string
			for k := range cfg {
				if k != "file" {
					key = k
				}
			}
			over := map[string]map[string]any{rule: cfg}
			_, err := lint.Run(ix, prof, over, []string{rule})
			require.Error(t, err, "%s must reject a scalar %s", rule, key)
			require.Contains(t, err.Error(), rule, "the error must name the rule")
			require.Contains(t, err.Error(), key, "the error must name the key")
		})
	}
}

// A non-string entry inside an otherwise valid list must also fail.
func TestNonStringListEntryFailsTheRun(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	over := map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{"Areas/**", 42}},
	}
	_, err := lint.Run(ix, prof, over, []string{"non-markdown-in-note-folders"})
	require.Error(t, err, "a non-string list entry must fail the run")
	require.Contains(t, err.Error(), "scopes")
	require.Contains(t, err.Error(), "non-markdown-in-note-folders")
}

// warning_types written as a bare string must fail the run — silently
// dropping it made severities differ from the config the user wrote, the
// exact silent-severity failure #29 set out to close, one shape away.
func TestScalarWarningTypesFailsTheRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"People/Snyk/Broken.md": "---\nname: Jane\n---\nx\n",
	})
	over := map[string]map[string]any{"frontmatter-schema": {"warning_types": "person"}}
	_, err := lint.Run(ix, prof, over, []string{"frontmatter-schema"})
	require.Error(t, err, "a non-list warning_types must fail the run")
	require.Contains(t, err.Error(), "warning_types")
	require.Contains(t, err.Error(), "frontmatter-schema")
}

// Non-string entries inside warning_types must be rejected, not dropped.
func TestNonStringWarningTypeEntryFailsTheRun(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	over := map[string]map[string]any{
		"frontmatter-schema": {"warning_types": []any{"person", 42}},
	}
	_, err := lint.Run(ix, prof, over, []string{"frontmatter-schema"})
	require.Error(t, err, "a non-string warning_types entry must be rejected")
	require.Contains(t, err.Error(), "warning_types")
}

// domain-split-folders reads its domain lists from arbitrary keys; those
// lists are config too and must be shape-checked like every other.
func TestDomainSplitFoldersRejectsWrongShapedLists(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for label, cfg := range map[string]map[string]any{
		"scalar value":     {"Projects": "Snyk"},
		"non-string entry": {"Projects": []any{"Snyk", 1}},
	} {
		t.Run(label, func(t *testing.T) {
			over := map[string]map[string]any{"domain-split-folders": cfg}
			_, err := lint.Run(ix, prof, over, []string{"domain-split-folders"})
			require.Error(t, err, "domain-split-folders must reject a %s", label)
			require.Contains(t, err.Error(), "Projects", "the error must name the key")
			require.Contains(t, err.Error(), "domain-split-folders")
		})
	}
}

// ---- the fix path validates the same config -------------------------------

// lint.Fix instantiates a rule from the same merged config as lint.Run; a
// config Run would reject must be rejected on the fix path too, or --fix
// becomes a validation bypass.
func TestFixPathValidatesConfigLikeRun(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"People/Snyk/Broken.md": "---\nname: Jane\n---\nx\n",
	})
	for label, over := range map[string]map[string]map[string]any{
		"scalar warning_types": {"frontmatter-schema": {"warning_types": "person"}},
		"unknown warning_type": {"frontmatter-schema": {"warning_types": []any{"meetings"}}},
	} {
		t.Run(label, func(t *testing.T) {
			f := lint.Finding{Rule: "frontmatter-schema", Path: "People/Snyk/Broken.md"}
			_, err := lint.Fix(ix, prof, over, f)
			require.Error(t, err, "Fix must reject the %s like Run does", label)
		})
	}

	// A malformed glob is rejected on the fix path too.
	f := lint.Finding{Rule: "non-markdown-in-note-folders", Path: "People/Snyk/Broken.md"}
	_, err := lint.Fix(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{"[unclosed"}}}, f)
	require.Error(t, err)
	require.Contains(t, err.Error(), "[unclosed")
}
