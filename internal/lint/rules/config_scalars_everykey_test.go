package rules_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// #33 routes every SCALAR-valued option through lint.CfgString/CfgInt/CfgBool.
// The spot checks in config_shapes_test.go cover a handful of rules; this is
// the other axis — every scalar key on every rule that reads one, the same
// grid config_shapes_everykey_test.go provides for the list-valued keys.
//
// Without it the new error branches were unexercised on 15 of the 24 read
// sites (measured as uncovered blocks at vaultwide.go:16, 20, 30, 34, 72, 93,
// 120, 128, 136, 146, 160, 174, 186 and links.go:40) — every one of them a
// key whose wrong type used to silently disable a check.
//
// Each case sets any key the factory reads BEFORE the one under test to a
// valid value, so the factory reaches the key instead of failing earlier.
type scalarCase struct {
	rule string
	key  string
	with map[string]any // other keys needed to reach the one under test
}

// scalarStringKeys is every string-valued option, per rule.
func scalarStringKeys() []scalarCase {
	return []scalarCase{
		{rule: "action-items-count-drift", key: "file"},
		{rule: "action-items-count-drift", key: "key"},
		{rule: "recipes-count-drift", key: "file"},
		{rule: "recipes-count-drift", key: "counts"},
		{rule: "recipes-count-drift", key: "key"},
		{rule: "recipes-index-links-complete", key: "file"},
		{rule: "recipes-index-links-complete", key: "lists"},
		{rule: "resources-cataloged-in-index", key: "file"},
		{rule: "resources-cataloged-in-index", key: "lists"},
		{rule: "projects-linked-from-master", key: "file"},
		{rule: "projects-linked-from-master", key: "lists"},
		{rule: "index-no-inventory", key: "file"},
		{rule: "log-entry-format", key: "file"},
		{rule: "log-action-vocab", key: "file"},
		{rule: "log-newest-first", key: "file"},
		// Defaults to enabled = false, so the file read is only reachable
		// with the rule switched on.
		{rule: "log-entry-bullets-flat", key: "file", with: map[string]any{"enabled": true}},
		{rule: "now-line-cap", key: "file"},
		{rule: "now-fixed-sections", key: "file"},
		{rule: "now-no-sync-sections", key: "file"},
		{rule: "now-active-projects-shape", key: "file"},
		{rule: "now-active-projects-shape", key: "section"},
		{rule: "no-drafts-folder", key: "dir"},
		{rule: "attendee-links-resolve-to-people", key: "folder"},
		{rule: "related-people-resolve-to-people", key: "folder"},
	}
}

// scalarIntKeys is every integer-valued option, per rule.
func scalarIntKeys() []scalarCase {
	return []scalarCase{
		{rule: "now-line-cap", key: "warn_at"},
		{rule: "now-line-cap", key: "error_at"},
		{rule: "now-active-projects-shape", key: "min_bullets"},
		{rule: "now-active-projects-shape", key: "max_bullets"},
	}
}

func (tc scalarCase) override(v any) map[string]map[string]any {
	cfg := map[string]any{}
	for k, val := range tc.with {
		cfg[k] = val
	}
	cfg[tc.key] = v
	return map[string]map[string]any{tc.rule: cfg}
}

// Every string-valued key must reject a non-string. A dropped value left the
// key at its default, and for the `file`-gated rules that default is "" —
// which turns the rule OFF without a word.
func TestEveryStringValuedKeyRejectsANonString(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range scalarStringKeys() {
		for label, bad := range map[string]any{
			"int":   42,
			"bool":  true,
			"float": 1.5,
			"list":  []any{"Now.md"},
			"map":   map[string]any{"Now.md": true},
		} {
			t.Run(tc.rule+"."+tc.key+"/"+label, func(t *testing.T) {
				_, err := lint.Run(ix, prof, tc.override(bad), []string{tc.rule})
				require.Errorf(t, err,
					"%s.%s as a %s must fail the run, not silently disable the check",
					tc.rule, tc.key, label)
				require.Contains(t, err.Error(), tc.key, "the error must name the key")
				require.Contains(t, err.Error(), tc.rule, "the error must name the rule")
				require.Contains(t, err.Error(), "want a string",
					"the message must say what was expected")
			})
		}
	}
}

// Every integer-valued key must reject a non-integer. A float is the case
// that matters most: the reader it replaced accepted float64 and truncated,
// so `warn_at = 59.9` silently became 59 — a cap the user never set.
func TestEveryIntValuedKeyRejectsANonInteger(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range scalarIntKeys() {
		for label, bad := range map[string]any{
			"string":         "60",
			"float":          60.5,
			"whole float":    60.0,
			"bool":           true,
			"list":           []any{60},
			"negative float": -1.5,
		} {
			t.Run(tc.rule+"."+tc.key+"/"+label, func(t *testing.T) {
				_, err := lint.Run(ix, prof, tc.override(bad), []string{tc.rule})
				require.Errorf(t, err, "%s.%s as a %s must fail the run", tc.rule, tc.key, label)
				require.Contains(t, err.Error(), tc.key, "the error must name the key")
				require.Contains(t, err.Error(), tc.rule, "the error must name the rule")
				require.Contains(t, err.Error(), "want an integer",
					"the message must say what was expected")
			})
		}
	}
}

// The other half of a fail-closed check: it must not reject config that IS
// usable. TOML decode delivers int64 and Go callers pass int, so both must
// be accepted on every integer key, and a plain string on every string key.
func TestEveryScalarKeyAcceptsItsValidTypes(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, tc := range scalarStringKeys() {
		t.Run(tc.rule+"."+tc.key+"/string", func(t *testing.T) {
			// "Now.md" is a real note in the clean vault, and a valid glob,
			// so it is honorable for file/lists/counts/key/dir/section alike.
			_, err := lint.Run(ix, prof, tc.override("Now.md"), []string{tc.rule})
			require.NoErrorf(t, err, "%s.%s must accept a string", tc.rule, tc.key)
		})
	}
	for _, tc := range scalarIntKeys() {
		for label, good := range map[string]any{"int64": int64(7), "int": 7} {
			t.Run(tc.rule+"."+tc.key+"/"+label, func(t *testing.T) {
				_, err := lint.Run(ix, prof, tc.override(good), []string{tc.rule})
				require.NoErrorf(t, err, "%s.%s must accept %s", tc.rule, tc.key, label)
			})
		}
	}
}

// The grid above is only as good as its completeness, and a grid maintained
// by hand goes stale the first time a rule gains a scalar option. This reads
// the rule sources and fails when a scalar key is read that no case covers.
func TestEveryScalarKeyReadInTheSourceIsInTheGrid(t *testing.T) {
	covered := map[string]bool{
		// Engine-level, validated in instantiate rather than a factory;
		// covered by TestEveryRuleRejectsANonBoolEnabled below.
		"enabled": true,
	}
	for _, tc := range append(scalarStringKeys(), scalarIntKeys()...) {
		covered[tc.key] = true
	}

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	readRe := regexp.MustCompile(`lint\.Cfg(?:String|Int|Bool)\(cfg, "([a-z_]+)"`)
	var seen []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for _, m := range readRe.FindAllStringSubmatch(string(src), -1) {
			seen = append(seen, m[1])
			require.Truef(t, covered[m[1]],
				"%s reads scalar config key %q, which no case in this file covers — "+
					"add it to scalarStringKeys/scalarIntKeys so its wrong-type "+
					"branch is exercised", f, m[1])
		}
	}
	require.NotEmpty(t, seen, "premise: the scan must find the scalar config reads")
}

// `enabled` is read by the engine for every rule, so the grid is every rule.
// A wrong-typed `enabled` must not leave the rule running (or, for the two
// rules that default to off, silently switch it on).
func TestEveryRuleRejectsANonBoolEnabled(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, id := range lint.RuleIDs() {
		for label, bad := range map[string]any{
			"quoted false": "false",
			"quoted true":  "true",
			"int":          1,
			"list":         []any{true},
		} {
			t.Run(id+"/"+label, func(t *testing.T) {
				_, err := lint.Run(ix, prof,
					map[string]map[string]any{id: {"enabled": bad}}, []string{id})
				require.Errorf(t, err, "%s: enabled as %s must fail the run", id, label)
				require.Contains(t, err.Error(), "enabled")
				require.Contains(t, err.Error(), id)
				require.Contains(t, err.Error(), "want a boolean")
			})
		}
	}
}

// The engine validates `enabled` before any factory runs, so the two rules
// that also read `enabled` themselves can never see a non-bool. Pinned
// because it is the reason their own error branches are unreachable: if the
// engine's check ever moves after instantiation, this test says so.
func TestEnabledIsRejectedByTheEngineBeforeTheFactoryReadsIt(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, id := range []string{"frontmatter-key-order", "log-entry-bullets-flat"} {
		t.Run(id, func(t *testing.T) {
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{id: {"enabled": "true"}}, []string{id})
			require.Error(t, err)
			require.Equal(t,
				`rule `+id+`: enabled: got string (true), want a boolean`, err.Error(),
				"the engine's message, not a factory's — the factory is never reached")
		})
	}
}

// A disabled rule's scalar config is not validated, matching the documented
// policy for lists and globs (TestDisabledRuleSkipsConfigValidation).
func TestDisabledRuleSkipsScalarConfigValidation(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	_, err := lint.Run(ix, prof, map[string]map[string]any{
		"now-line-cap": {"enabled": false, "file": 42, "warn_at": "sixty"},
	}, nil)
	require.NoError(t, err, "disabled rules are skipped before their config is read")
}

// --fix must not be a validation bypass for the scalar keys either: it runs
// through the same instantiate path, so a wrong-typed option stops the repair.
func TestFixRejectsAWrongTypedScalarOption(t *testing.T) {
	ix, prof, f := fixableTopicsFinding(t)
	_, err := lint.Fix(ix, prof,
		map[string]map[string]any{"person-topics-kebab-case": {"enabled": "false"}}, f)
	require.Error(t, err, "--fix must reject a config Run rejects")
	require.Contains(t, err.Error(), "enabled")
}

// The engine reports the FIRST offending rule, and rule order is sorted, so
// the message a user sees must be the same on every run. A map-ordered
// instantiate would make this error text flaky and the fix unreproducible.
func TestConfigErrorMessageIsDeterministicAcrossRules(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	over := map[string]map[string]any{
		"now-line-cap":              {"warn_at": "sixty"},
		"action-items-count-drift":  {"file": 42},
		"log-newest-first":          {"file": true},
		"now-active-projects-shape": {"min_bullets": 1.5},
	}
	_, first := lint.Run(ix, prof, over, nil)
	require.Error(t, first)
	for i := 0; i < 25; i++ {
		_, err := lint.Run(ix, prof, over, nil)
		require.Error(t, err)
		require.Equal(t, first.Error(), err.Error(),
			"the reported config error must not depend on map iteration order")
	}
}

// ---- the severity check, beyond the one rule the spot check uses ----------

// #34 validates `severity` in the engine, so the contract is every rule's.
// The live vault's only lint overrides are severity-only, which makes a
// typo'd severity the most likely real-world config error of the three.
//
// The two rules the rdegges profile ships with `enabled = false` are the
// exception: the enabled-skip runs first, so their config is never read
// (TestDisabledRuleSkipsSeverityValidation). They are checked with the rule
// switched on below, which proves the skip is the ONLY reason they differ.
var offByDefault = map[string]bool{
	"frontmatter-key-order":  true,
	"log-entry-bullets-flat": true,
}

func badSeverities() map[string]any {
	return map[string]any{
		"wrong case":   "Warning",
		"padded":       " error ",
		"upper":        "ERROR",
		"info":         "info",
		"float":        1.0,
		"map":          map[string]any{"error": true},
		"empty string": "",
	}
}

func TestUnrecognizedSeverityIsRejectedForEveryEnabledRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, id := range lint.RuleIDs() {
		for label, bad := range badSeverities() {
			t.Run(id+"/"+label, func(t *testing.T) {
				cfg := map[string]any{"severity": bad}
				if offByDefault[id] {
					cfg["enabled"] = true
				}
				_, err := lint.Run(ix, prof,
					map[string]map[string]any{id: cfg}, []string{id})
				require.Errorf(t, err, "%s: severity %v must fail the run", id, bad)
				require.Contains(t, err.Error(), "severity")
				require.Contains(t, err.Error(), id)
			})
		}
	}
}

// The flip side of the exception above, stated where it can be found: a rule
// the profile ships disabled keeps a typo'd severity to itself. This is the
// widest reach of the enabled-skip — every OTHER shape of broken config on a
// disabled rule needs an explicit `enabled = false`, but these two need
// nothing at all, so a severity typo on them is silent on a default install.
func TestKnownGap_SeverityTypoIsSilentOnAProfileDisabledRule(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for id := range offByDefault {
		for label, bad := range badSeverities() {
			t.Run(id+"/"+label, func(t *testing.T) {
				_, err := lint.Run(ix, prof,
					map[string]map[string]any{id: {"severity": bad}}, []string{id})
				require.NoErrorf(t, err,
					"GAP: %s is disabled in the profile, so its severity is never read", id)
			})
		}
	}
}

// A severity typo in the PROFILE's own table must fail too: the check runs on
// the merged config, so it cannot be a vault-override-only guard.
func TestUnrecognizedSeverityInTheProfileTableFailsTheRun(t *testing.T) {
	ix, _, _ := buildVault(t, cleanVault())
	prof := loadTinyProfile(t, "[lint.empty-note]\nseverity = \"warn\"\n")
	_, err := lint.Run(ix, prof, nil, []string{"empty-note"})
	require.Error(t, err, "a shipped profile's severity typo must not be dropped")
	require.Contains(t, err.Error(), "severity")
	require.Contains(t, err.Error(), "empty-note")

	// And a vault override can repair a profile whose severity is wrong,
	// because the override wins the merge.
	_, err = lint.Run(ix, prof,
		map[string]map[string]any{"empty-note": {"severity": "warning"}}, []string{"empty-note"})
	require.NoError(t, err)
}

// Both accepted spellings must actually take effect in BOTH directions. The
// spot check only proves promotion; a demotion that silently kept `error`
// would leave a user's `--fail-on error` run red forever.
func TestBothSeveritySpellingsAreHonoredInBothDirections(t *testing.T) {
	// wikilink-resolves reports errors by default.
	files := map[string]string{"Areas/Personal/A.md": "see [[Nowhere]]\n"}
	ix, prof := buildVaultWith(t, "rdegges", files)

	fs, err := lint.Run(ix, prof, nil, []string{"wikilink-resolves"})
	require.NoError(t, err)
	require.NotEmpty(t, fs, "premise: the broken link is a finding")
	require.Positive(t, severities(fs)[lint.Error], "premise: it is an error by default")

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"wikilink-resolves": {"severity": "warning"}},
		[]string{"wikilink-resolves"})
	require.NoError(t, err)
	require.Positive(t, severities(fs)[lint.Warning], "the demotion must apply: %+v", fs)
	require.Zero(t, severities(fs)[lint.Error], "%+v", fs)
}

// ---- the cross-rule (peer) read, beyond the happy path -------------------

// The reader wraps the source rule's error, so the message must name BOTH
// tables: the rule that failed and the table the bad value lives in. Naming
// only root-file-name-case would send the user to a table that is fine.
func TestPeerConfigErrorNamesTheSourceTable(t *testing.T) {
	ix, _, _ := buildVault(t, cleanVault())
	for label, cfg := range map[string]string{
		"scalar":           "[lint.root-canonical-only]\nfiles = \"Now.md\"\n",
		"non-string entry": "[lint.root-canonical-only]\nfiles = [\"Now.md\", 7]\n",
		"map":              "[lint.root-canonical-only.files]\nNow = \"md\"\n",
	} {
		t.Run(label, func(t *testing.T) {
			prof := loadTinyProfile(t, cfg)
			_, err := lint.Run(ix, prof, nil, []string{"root-file-name-case"})
			require.Error(t, err)
			require.Contains(t, err.Error(), "root-file-name-case",
				"the error must name the rule that failed")
			require.Contains(t, err.Error(), "root-canonical-only.files",
				"the error must name the table the user has to edit")
		})
	}
}

// A Go caller passes []string where TOML gives []any; the peer read must
// honor both, or the reader goes quiet for one of its two callers.
func TestPeerConfigHonorsBothListShapes(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "now.md"), []byte("x\n"), 0o644))
	prof := loadTinyProfile(t, "")
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)

	for label, files := range map[string]any{
		"[]any":    []any{"Now.md"},
		"[]string": []string{"Now.md"},
	} {
		t.Run(label, func(t *testing.T) {
			fs, err := lint.Run(ix, prof,
				map[string]map[string]any{"root-canonical-only": {"files": files}},
				[]string{"root-file-name-case"})
			require.NoError(t, err)
			require.Len(t, fs, 1, "the reader must honor a %s peer list: %+v", label, fs)
			require.Equal(t, "root-file-name-case", fs[0].Rule)
		})
	}
}

// Disabling the READER skips its peer validation, the same way it skips its
// own config. Pinned so the two halves of the enabled-skip stay symmetric.
func TestDisabledReaderSkipsPeerConfigValidation(t *testing.T) {
	ix, _, _ := buildVault(t, cleanVault())
	prof := loadTinyProfile(t, "[lint.root-canonical-only]\nfiles = \"Now.md\"\n")
	_, err := lint.Run(ix, prof,
		map[string]map[string]any{"root-file-name-case": {"enabled": false}},
		[]string{"root-file-name-case"})
	require.NoError(t, err, "a disabled reader consumes nothing, so it validates nothing")
}

// An override that empties the peer list turns the reader off — `files = []`
// means "unconfigured", exactly as it does for the source rule itself
// (TestEmptyListIsHonoredNotRejected). Pinned because it is the one way to
// silence root-file-name-case without naming it.
func TestEmptyPeerListDisablesTheReader(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "now.md"), []byte("x\n"), 0o644))
	prof := loadTinyProfile(t, "[lint.root-canonical-only]\nfiles = [\"Now.md\"]\n")
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)

	fs, err := lint.Run(ix, prof, nil, []string{"root-file-name-case"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "premise: 'now.md' is a case variant of 'Now.md': %+v", fs)

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"root-canonical-only": {"files": []any{}}},
		[]string{"root-file-name-case"})
	require.NoError(t, err, "an empty list is a usable shape")
	require.Empty(t, fs, "an empty canonical set leaves nothing to compare: %+v", fs)
}

// ---- known fail-open gaps this change did not close ----------------------
//
// These pin CURRENT behavior, not desired behavior: a wrong TYPE now fails
// closed, but a type-correct value that cannot be honored still disables a
// check in silence. If a later change closes one, the test below fails —
// that failure is the fix landing, and the test should be inverted, not
// deleted. (Same convention as the #29-#32 KnownGap pins.)

// GAP: an empty string is the "unconfigured" sentinel for every `file`-gated
// rule, so `file = ""` silently switches the rule OFF — the exact outcome
// #33 closed for wrong types, reachable with a well-typed value. `severity`
// rejects "" (TestUnrecognizedSeverityIsRejectedForEveryRule); `file` does
// not, and docs/LINT-RULES.md does not mention it.
func TestKnownGap_EmptyStringDisablesAFileGatedRule(t *testing.T) {
	long := "# Now\n" + strings.Repeat("- line\n", 400)
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{"Now.md": long})

	fs, err := lint.Run(ix, prof, nil, []string{"now-line-cap"})
	require.NoError(t, err)
	require.NotEmpty(t, fs, "premise: a 400-line Now.md breaks the cap")

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"now-line-cap": {"file": ""}}, []string{"now-line-cap"})
	require.NoError(t, err, "GAP: an empty `file` does not fail the run")
	require.Empty(t, fs, "GAP: the cap check is silently disabled instead: %+v", fs)
}

// GAP: `folder = ""` is a well-typed value that makes every path match, so
// the attendee/related-people checks pass on notes they should reject. This
// one is worse than the empty `file` above: the rule still runs and still
// reports clean, so there is no "rule skipped" signal anywhere.
func TestKnownGap_EmptyFolderMakesLinkResolutionAlwaysPass(t *testing.T) {
	files := map[string]string{
		"Meetings/Snyk/2026/05/06/1100 - Sync.md": "---\ndate: 2026-05-06\ntime: \"11:00\"\n" +
			"duration: 30\ntype: meeting\nattendees:\n  - \"[[Elsewhere]]\"\n---\n## Notes\n- x\n",
		"Areas/Personal/Elsewhere.md": "x\n",
	}
	ix, prof := buildVaultWith(t, "rdegges", files)

	fs, err := lint.Run(ix, prof, nil, []string{"attendee-links-resolve-to-people"})
	require.NoError(t, err)
	require.NotEmpty(t, fs, "premise: an attendee outside People/ is a finding")

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"attendee-links-resolve-to-people": {"folder": ""}},
		[]string{"attendee-links-resolve-to-people"})
	require.NoError(t, err, "GAP: an empty `folder` does not fail the run")
	require.Empty(t, fs, "GAP: every path is 'under' the empty prefix: %+v", fs)
}

// GAP: the integer caps are type-checked but not range-checked, so an
// inverted pair is accepted. It fails loud rather than silent (every note
// trips the rule), which is why it is a gap and not a defect proof.
func TestKnownGap_InvertedNumericCapsAreAccepted(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Now.md": "# Now\n" + strings.Repeat("- line\n", 70),
	})
	fs, err := lint.Run(ix, prof, map[string]map[string]any{
		"now-line-cap": {"warn_at": 80, "error_at": 60},
	}, []string{"now-line-cap"})
	require.NoError(t, err, "GAP: warn_at > error_at is not rejected")
	require.Len(t, fs, 1, "%+v", fs)
	require.Equal(t, lint.Error, fs[0].Severity,
		"GAP: the hard cap is below the target, so a 70-line file is an error")
}

// ---- log-entry-bullets-flat: the off-by-default rule --------------------

// The only rule this change left with an unexercised body: it ships disabled
// on both profiles, so nothing instantiated it and its factory's new `file`
// read was dead in tests. Enabling it must build the rule AND check it.
func TestLogEntryBulletsFlatRunsWhenEnabled(t *testing.T) {
	nested := "## [2026-05-06] update | thing\n- top\n  - nested\n\n" +
		"## [2026-05-05] update | other\n- flat\n"
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{"log.md": nested})

	fs, err := lint.Run(ix, prof, nil, []string{"log-entry-bullets-flat"})
	require.NoError(t, err)
	require.Empty(t, fs, "premise: the rule ships off: %+v", fs)

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"log-entry-bullets-flat": {"enabled": true}},
		[]string{"log-entry-bullets-flat"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "only the nested entry is reported: %+v", fs)
	require.Equal(t, lint.Warning, fs[0].Severity)
	require.Equal(t, "log.md", fs[0].Path)
	require.Contains(t, fs[0].Message, "flat")

	// The `file` default is log.md; pointing it elsewhere must follow.
	ix2, prof2 := buildVaultWith(t, "rdegges", map[string]string{"Archive/Personal/old-log.md": nested})
	fs, err = lint.Run(ix2, prof2, map[string]map[string]any{
		"log-entry-bullets-flat": {"enabled": true, "file": "Archive/Personal/old-log.md"},
	}, []string{"log-entry-bullets-flat"})
	require.NoError(t, err)
	require.Len(t, fs, 1, "%+v", fs)
	require.Equal(t, "Archive/Personal/old-log.md", fs[0].Path)
}

// loadTinyProfile builds a minimal on-disk profile carrying only the given
// [lint.*] tables, so a test can exercise config that arrives through TOML
// decode rather than a hand-built Go map.
func loadTinyProfile(t *testing.T, lintCfg string) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	manifest := "schema_version = 1\nname = \"tiny\"\nscaffold = [\"Areas\"]\n\n" +
		"[[types]]\nname = \"area\"\nscope = [\"Areas/**\"]\n\n" + lintCfg
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	p, err := profile.Load(dir)
	require.NoError(t, err)
	return p
}
