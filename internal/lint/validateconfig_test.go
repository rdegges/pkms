// External test package: rules imports lint, so registering the real rule
// set from inside package lint would be an import cycle. Doctor's
// lint-config check (SPEC §34) reads the same registry through the same
// blank import, so these tests exercise the production registry.
package lint_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	_ "github.com/rdegges/pkms/internal/lint/rules" // rule registrations
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// emptyIndex is enough to drive lint.Run for the error comparison below:
// Run instantiates before it checks anything, so a config error never
// depends on vault content.
func emptyIndex(t *testing.T) *vault.Index {
	t.Helper()
	ix, err := vault.BuildIndex(t.TempDir(), vault.WalkOptions{})
	require.NoError(t, err)
	return ix
}

// The shipped profiles must validate with no overrides at all. This is the
// precondition for doctor's lint-config check being green out of the box —
// if it fails, every `pkms doctor` on a fresh install reports a failure.
func TestValidateConfigAcceptsEveryShippedProfile(t *testing.T) {
	builtins := profile.Builtins()
	require.NotEmpty(t, builtins, "no builtin profiles: the check below proves nothing")
	for _, name := range builtins {
		t.Run(name, func(t *testing.T) {
			prof, err := profile.Load(name)
			require.NoError(t, err)
			require.NoError(t, lint.ValidateConfig(prof, nil),
				"profile %q ships a lint config doctor would fail on", name)
		})
	}
}

// A gate counts as installed only after it is observed rejecting a seeded
// violation — for EVERY rule, not just the handful a hand-written table
// covers. `severity` is validated for every enabled rule, so seeding a junk
// severity (with enabled forced on, since some rules ship disabled) is a
// uniform violation. An empty registry would make doctor's green sentence
// ("every lint rule instantiates") vacuously true, so the count is guarded.
func TestValidateConfigRejectsASeededViolationInEveryRule(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ids := lint.RuleIDs()
	require.NotEmpty(t, ids, "no rules registered: doctor's lint-config check would be vacuous")

	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			ov := map[string]map[string]any{id: {"enabled": true, "severity": "loud"}}
			err := lint.ValidateConfig(prof, ov)
			require.Error(t, err, "a junk severity on %s is accepted", id)
			require.Contains(t, err.Error(), id, "the error must name the offending rule")
		})
	}
}

// SPEC §34: doctor's check "runs the exact validation path `pkms lint` runs
// ... so the two can never disagree about what 'valid config' means". This
// pins that equivalence across every rejection class the engine has, so a
// future validation step added to only one of the two paths fails here
// rather than silently making doctor green on a config lint refuses.
func TestValidateConfigAgreesWithLintRunOnEveryConfigShape(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ix := emptyIndex(t)

	cases := []struct {
		name      string
		overrides map[string]map[string]any
		wantErr   bool
		// agreementOnly: whether the engine SHOULD reject this shape is an
		// open policy question (issue #42 and siblings). The equivalence is
		// not: assert only that, so closing #42 does not make this lie.
		agreementOnly bool
	}{
		{name: "no overrides", overrides: nil},
		{
			name:      "severity-only override (the shape real vaults use)",
			overrides: map[string]map[string]any{"orphan-notes": {"severity": "warning"}},
		},
		{
			// The profile ships frontmatter-key-order disabled; instantiate
			// skips a disabled rule's config on purpose, so lint runs and
			// doctor must be green too. `orders` is a key this rule really
			// consumes (a scalar there is rejected once enabled — see the
			// paired case below), so the green verdict here is the
			// short-circuit and not an unread key.
			name:          "broken config on a profile-disabled rule",
			overrides:     map[string]map[string]any{"frontmatter-key-order": {"orders": "meeting"}},
			agreementOnly: true,
		},
		{
			name:          "broken config the override explicitly disables",
			overrides:     map[string]map[string]any{"orphan-notes": {"enabled": false, "scopes": []any{"[unclosed"}}},
			agreementOnly: true,
		},
		{
			// A table naming no registered rule is a config error on both
			// surfaces — a typo'd id was silently inert before (#46), the
			// "nothing to do" green nobody notices.
			name:      "table for an unregistered rule id",
			overrides: map[string]map[string]any{"no-such-rule": {"severity": "warning"}},
			wantErr:   true,
		},
		{
			name:      "override re-enables a disabled rule carrying broken config",
			overrides: map[string]map[string]any{"frontmatter-key-order": {"enabled": true, "orders": "meeting"}},
			wantErr:   true,
		},
		{
			name:      "malformed scope glob",
			overrides: map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{"[unclosed"}}},
			wantErr:   true,
		},
		{
			name:      "scalar where a list is required",
			overrides: map[string]map[string]any{"no-junk-files": {"patterns": "*.bak"}},
			wantErr:   true,
		},
		{
			name:      "non-string entry in a list",
			overrides: map[string]map[string]any{"no-junk-files": {"patterns": []any{"*.bak", 7}}},
			wantErr:   true,
		},
		{
			name:      "unrecognized severity spelling",
			overrides: map[string]map[string]any{"orphan-notes": {"severity": "err"}},
			wantErr:   true,
		},
		{
			name:      "warning_types names an undeclared note type",
			overrides: map[string]map[string]any{"frontmatter-schema": {"warning_types": []any{"nosuchtype"}}},
			wantErr:   true,
		},
		{
			name:      "non-bool enabled",
			overrides: map[string]map[string]any{"orphan-notes": {"enabled": "yes"}},
			wantErr:   true,
		},
		{
			// TOML floats decode as float64; an int option must reject them
			// rather than truncate (issue #33).
			name:      "float where an integer is required",
			overrides: map[string]map[string]any{"now-line-cap": {"warn_at": 60.5}},
			wantErr:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			validateErr := lint.ValidateConfig(prof, tc.overrides)
			_, runErr := lint.Run(ix, prof, tc.overrides, nil)

			// Same verdict AND same wording: the user is told the same thing
			// by both commands.
			require.Equal(t, errText(runErr), errText(validateErr),
				"doctor and `pkms lint` disagree about this config")
			if tc.agreementOnly {
				return
			}
			if tc.wantErr {
				require.Error(t, validateErr, "doctor would report this config healthy")
				require.Error(t, runErr, "`pkms lint` would run on this config")
			} else {
				require.NoError(t, validateErr, "doctor would fail a config lint accepts")
				require.NoError(t, runErr)
			}
		})
	}
}

// A `--rules` run validates a subset; doctor validates all of them. The
// asymmetry must only ever go one way — doctor is never more permissive
// than a narrowed lint run, or a config that breaks `pkms lint --rules X`
// could still pass doctor.
func TestValidateConfigIsNeverMorePermissiveThanANarrowedLintRun(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ix := emptyIndex(t)
	ov := map[string]map[string]any{"orphan-notes": {"severity": "loud"}}

	_, narrowErr := lint.Run(ix, prof, ov, []string{"orphan-notes"})
	require.Error(t, narrowErr, "premise: the narrowed run rejects this config")
	require.Error(t, lint.ValidateConfig(prof, ov),
		"a config that breaks `pkms lint --rules orphan-notes` must fail doctor too")
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
