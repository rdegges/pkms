// SPEC §35 (issue #46): a config table whose key names no registered rule
// is a config error on every surface, never silently inert. External test
// package for the same reason as validateconfig_test.go — the real registry
// only exists behind the rules package's blank import.
package lint_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	_ "github.com/rdegges/pkms/internal/lint/rules" // rule registrations
	"github.com/rdegges/pkms/internal/profile"
)

// profileWithLintTables writes a minimal on-disk profile carrying the given
// `[lint.<id>]` tables. The profile half of the check has its own reachable
// surface — `profile.Load` accepts a directory path, so a hand-written or
// stale profile can name a rule that no longer exists.
func profileWithLintTables(t *testing.T, tables ...string) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	src := "schema_version = 1\nname = \"tmp\"\n"
	for _, id := range tables {
		src += "\n[lint.\"" + id + "\"]\nenabled = true\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(src), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err, "premise: the profile itself must load; only the "+
		"rule id is meant to be wrong")
	return prof
}

// The profile half of the check. A `[lint.<id>]` table naming no registered
// rule must fail even with no vault overrides at all — otherwise a rule
// rename degrades every user of that profile to a silent no-op.
func TestUnknownRuleTableInAProfileIsRejected(t *testing.T) {
	prof := profileWithLintTables(t, "empty-note", "no-such-rule")

	err := lint.ValidateConfig(prof, nil)
	require.Error(t, err, "a profile table for an unregistered rule was accepted")
	require.Contains(t, err.Error(), "no-such-rule", "the error must name the offending id")

	_, runErr := lint.Run(emptyIndex(t), prof, nil, nil)
	require.Error(t, runErr, "`pkms lint` must reject what doctor rejects")
	require.Equal(t, err.Error(), runErr.Error(), "the two surfaces must say the same thing")
}

// A profile whose lint tables all name registered rules is unaffected: the
// check must not reject the shape every real profile uses.
func TestKnownRuleTablesInAProfileAreAccepted(t *testing.T) {
	prof := profileWithLintTables(t, "empty-note", "no-junk-files")
	require.NoError(t, lint.ValidateConfig(prof, nil))
	_, err := lint.Run(emptyIndex(t), prof, nil, nil)
	require.NoError(t, err)
}

// SPEC §35: the check runs "before and independently of `--rules` scoping".
// Narrowing a run to a rule that IS registered must not let a stale table
// slip through — that is how `pkms lint --rules X` in a cron job would go on
// reporting green over a config nobody validated.
func TestUnknownRuleTableIsRejectedRegardlessOfRulesScoping(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ov := map[string]map[string]any{"orphan-note": {"severity": "warning"}}
	ix := emptyIndex(t)

	for _, only := range [][]string{nil, {"empty-note"}, {"orphan-notes"}} {
		_, runErr := lint.Run(ix, prof, ov, only)
		require.Errorf(t, runErr, "--rules %v bypassed the unknown-table check", only)
		require.Contains(t, runErr.Error(), "orphan-note", "--rules %v", only)
	}
}

// `enabled = false` short-circuits a rule's own config validation on purpose.
// It must NOT short-circuit the key check: disabling a rule that no longer
// exists is exactly the stale-config case §35 exists to catch.
func TestUnknownRuleTableIsRejectedEvenWhenItDisablesTheRule(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ov := map[string]map[string]any{"no-such-rule": {"enabled": false}}

	err = lint.ValidateConfig(prof, ov)
	require.Error(t, err, "a disabled table for an unregistered rule was accepted")
	require.Contains(t, err.Error(), "no-such-rule")
}

// SPEC §35: "unknown ids are reported deterministically (sorted, first
// named)". Go randomizes map iteration, so an unsorted implementation
// produces a different message per run — a moving error that breaks scripted
// consumers and makes the failure look intermittent. The union of BOTH
// sources is sorted, not each map separately.
func TestUnknownRuleTableErrorIsDeterministicAcrossProfileAndOverrides(t *testing.T) {
	prof := profileWithLintTables(t, "zz-unknown", "mm-unknown", "empty-note")
	ov := map[string]map[string]any{
		"aa-unknown": {"severity": "warning"},
		"nn-unknown": {"severity": "warning"},
	}

	first := lint.ValidateConfig(prof, ov)
	require.Error(t, first)
	require.Contains(t, first.Error(), "aa-unknown",
		"the alphabetically first unknown id across profile AND overrides must be named")

	// 50 iterations is enough to make a map-order-dependent message fail
	// essentially every run rather than one in a hundred.
	for i := 0; i < 50; i++ {
		require.Equal(t, first.Error(), lint.ValidateConfig(prof, ov).Error(),
			"the reported id changes between runs")
	}
}

// SPEC §35: "`lint`, `lint --fix`, and doctor agree by construction". Fix
// instantiates with only=[the finding's rule], so it is the surface most
// likely to drift back to accepting a table the other two reject — and it
// writes to the vault.
func TestFixRejectsAnUnknownRuleTable(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ov := map[string]map[string]any{"no-such-rule": {"severity": "warning"}}

	_, fixErr := lint.Fix(emptyIndex(t), prof, ov, lint.Finding{
		Rule: "frontmatter-present", Path: "People/Snyk/X.md", Fixable: true,
	})
	require.Error(t, fixErr, "--fix must not be a validation bypass")
	require.Contains(t, fixErr.Error(), "no-such-rule")
}
