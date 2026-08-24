package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
)

// Doctor's `lint-config` check (SPEC §34). The check's green sentence is
// "the vault's merged lint config instantiates every registered rule
// cleanly, so a default `pkms lint` run cannot exit 2 on config". These
// tests attack that sentence at the real CLI: the two commands must agree,
// the failure must be attributed to the right vault, a profile-sourced
// break must count, and an unloadable profile must leave the check ABSENT
// rather than green.

// doctorReportJSON runs `pkms doctor --json` and decodes it through the
// production checkResult type, so a field rename breaks here too.
func doctorReportJSON(t *testing.T) (checks []checkResult, summary map[string]int, out string, err error) {
	t.Helper()
	out, err = runCLI(t, "doctor", "--json")
	var payload struct {
		Checks  []checkResult  `json:"checks"`
		Summary map[string]int `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload), "doctor --json must always emit a payload: %s", out)
	return payload.Checks, payload.Summary, out, err
}

// lintConfigChecks returns every lint-config entry in a doctor report.
func lintConfigChecks(checks []checkResult) []checkResult {
	var out []checkResult
	for _, c := range checks {
		if c.Name == "lint-config" {
			out = append(out, c)
		}
	}
	return out
}

// lintRefusesToRun reports whether `pkms lint` treats the current config as
// a config error (exit 2) rather than findings (exit 1) or clean (exit 0).
func lintRefusesToRun(t *testing.T) bool {
	t.Helper()
	_, err := runCLI(t, "lint")
	return err != nil && !errors.Is(err, errFindings)
}

// The load-bearing §34 claim on the real surfaces: doctor's lint-config
// verdict matches whether `pkms lint` refuses to run, for every class of
// config the engine rejects and for the shapes it accepts. If these ever
// diverge, doctor certifies a vault whose linter is dead (or fails a vault
// that lints fine).
func TestDoctorLintConfigVerdictMatchesLintAtTheCLI(t *testing.T) {
	cases := []struct {
		name     string
		rule     string // "" = no override at all
		body     string
		wantFail bool
		// agreementOnly: whether this config SHOULD be rejected is an open
		// policy question (issue #42 and siblings — a disabled rule's config
		// is not read). Whatever the answer, the two commands must give the
		// same one, so only that is asserted and closing #42 will not make
		// this test lie.
		agreementOnly bool
	}{
		{name: "no override"},
		{name: "severity only", rule: "orphan-notes", body: `severity = "warning"`},
		{
			name: "broken config on a rule the profile disables",
			rule: "frontmatter-key-order", body: `orders = "meeting"`,
			agreementOnly: true,
		},
		{
			name: "malformed scope glob", rule: "non-markdown-in-note-folders",
			body: `scopes = ["[unclosed"]`, wantFail: true,
		},
		{
			name: "scalar where a list belongs", rule: "no-junk-files",
			body: `patterns = "*.bak"`, wantFail: true,
		},
		{
			name: "unrecognized severity", rule: "orphan-notes",
			body: `severity = "loud"`, wantFail: true,
		},
		{
			name: "non-bool enabled", rule: "orphan-notes",
			body: `enabled = "yes"`, wantFail: true,
		},
		{
			name: "float for an integer option", rule: "now-line-cap",
			body: `warn_at = 60.5`, wantFail: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
			if tc.rule != "" {
				appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), tc.rule, tc.body)
			}

			lintDead := lintRefusesToRun(t)
			checks, summary, out, err := doctorReportJSON(t)
			got := lintConfigChecks(checks)
			require.Len(t, got, 1, "exactly one lint-config entry per vault: %s", out)

			// The invariant, asserted for every case: doctor's verdict is
			// whatever `pkms lint` does with the same config.
			require.Equal(t, lintDead, got[0].Status == "fail",
				"doctor says %q while `pkms lint` refuses=%v — the two surfaces disagree: %s",
				got[0].Status, lintDead, out)
			if tc.agreementOnly {
				return
			}
			require.Equal(t, tc.wantFail, lintDead,
				"premise changed: `pkms lint` no longer treats this config the expected way")

			if tc.wantFail {
				require.Error(t, err, "a failing check must make doctor exit non-zero")
				require.Equal(t, 1, summary["fail"], "only lint-config may fail here: %s", out)
				require.Contains(t, got[0].Detail, tc.rule,
					"the detail must name the rule so the user can find the table")
				require.Equal(t, "lintv", got[0].Vault, "the failure must be attributed to the vault")
			} else {
				require.NoError(t, err, out)
				require.Equal(t, "ok", got[0].Status, out)
				require.NotEmpty(t, got[0].Detail, "the green check must say what it proved")
			}
		})
	}
}

// A rule's config can be broken by the PROFILE's own `[lint.*]` table with
// no vault override in sight — profile.Load validates type scope globs but
// never lint tables, so before #37 doctor had no surface that noticed. The
// `profile` check must stay green here: that proves lint-config is what
// caught it, not a load failure.
func TestDoctorFailsWhenTheProfilesOwnLintTableIsBroken(t *testing.T) {
	cfgPath := testEnv(t)
	profDir := filepath.Join(t.TempDir(), "prof")
	require.NoError(t, os.MkdirAll(profDir, 0o755))
	manifest := `schema_version = 1
name = "custom"
scaffold = ["Areas"]

[[types]]
name = "area"
scope = ["Areas/**"]

[lint.orphan-notes]
enabled = "yes"
`
	require.NoError(t, os.WriteFile(filepath.Join(profDir, "profile.toml"), []byte(manifest), 0o644))
	vaultDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "Areas"), 0o755))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "customv", Path: vaultDir, Profile: profDir,
	}))

	// Premise: this config kills `pkms lint`.
	require.True(t, lintRefusesToRun(t), "premise: lint must refuse this profile's lint table")

	checks, summary, out, err := doctorReportJSON(t)
	require.Error(t, err, "doctor must not certify a vault whose linter cannot run: %s", out)
	require.Equal(t, 1, summary["fail"], "exactly one failure — the lint config: %s", out)

	got := lintConfigChecks(checks)
	require.Len(t, got, 1, out)
	require.Equal(t, "fail", got[0].Status, out)
	require.Contains(t, got[0].Detail, "orphan-notes", "the rule must be named: %s", out)
	require.Contains(t, got[0].Detail, "enabled", "the offending key must be named: %s", out)

	for _, c := range checks {
		if c.Name == "profile" {
			require.Equal(t, "ok", c.Status,
				"the profile loads: lint-config is what must catch this, not profile: %s", out)
		}
	}
}

// SPEC §34: the check is "skipped (absent, not green) when the profile
// itself fails to load". A green lint-config line under an unloadable
// profile would be a claim nothing verified.
func TestDoctorOmitsLintConfigWhenTheProfileCannotLoad(t *testing.T) {
	cfgPath := testEnv(t)
	profDir := filepath.Join(t.TempDir(), "prof")
	require.NoError(t, os.MkdirAll(profDir, 0o755))
	manifest := `schema_version = 1
name = "custom"
scaffold = ["Areas"]

[[types]]
name = "area"
scope = ["Areas/[unclosed"]
`
	require.NoError(t, os.WriteFile(filepath.Join(profDir, "profile.toml"), []byte(manifest), 0o644))
	vaultDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "Areas"), 0o755))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "customv", Path: vaultDir, Profile: profDir,
	}))

	checks, summary, out, err := doctorReportJSON(t)
	require.Error(t, err, out)
	require.Empty(t, lintConfigChecks(checks),
		"lint-config must be absent (not green, not fail) when the profile cannot load: %s", out)
	require.Equal(t, 1, summary["fail"], "only the profile check may fail: %s", out)
}

// Per-vault attribution: one broken vault must not color a healthy one, and
// the healthy vault must still be checked (the loop must not abort).
func TestDoctorLintConfigIsPerVault(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")

	second := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(second, "Areas", "Personal"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(second, "Areas", "Personal", "n.md"), []byte("x\n"), 0o644))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "brokenv", Path: second, Profile: "rdegges",
	}))
	// The override lands under the LAST [[vaults]] table, i.e. brokenv.
	appendVaultLintOverride(t, cfgPath, "non-markdown-in-note-folders", `scopes = ["[unclosed"]`)

	checks, summary, out, err := doctorReportJSON(t)
	require.Error(t, err, out)
	require.Equal(t, 1, summary["fail"], "only the broken vault may fail: %s", out)

	byVault := map[string]checkResult{}
	for _, c := range lintConfigChecks(checks) {
		byVault[c.Vault] = c
	}
	require.Len(t, byVault, 2, "every vault with a loadable profile gets one entry: %s", out)
	require.Equal(t, "ok", byVault["lintv"].Status, "the healthy vault must stay green: %s", out)
	require.Equal(t, "fail", byVault["brokenv"].Status, out)
	require.Contains(t, byVault["brokenv"].Detail, "[unclosed", out)
}

// Doctor's report is terminal-bound and the lint-config check routes
// CONFIG-SUPPLIED strings into it. doctor.go's policy is explicit twice
// ("untrusted bytes headed for a terminal ... always printed escaped"):
// every offending value in the lint.Cfg*/instantiate messages is printed
// %q, so a config value carrying ESC can never reach stdout raw and erase
// the failure lines above it. The invariant asserted is
// wording-independent: no raw control bytes in the report.
func TestDoctorReportNeverEmitsRawControlBytesFromLintConfig(t *testing.T) {
	// A config value carrying ESC + BEL, written through real TOML escapes.
	// TOML \u escapes, so the source file itself stays clean text; the
	// decoded value carries ESC and BEL.
	const hostile = `"\u001B[2Jcleared\u0007"`

	// Already honored: the malformed-glob message uses %q.
	t.Run("malformed glob (policy already honored)", func(t *testing.T) {
		setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
		appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
			"non-markdown-in-note-folders", `scopes = [`+hostile+`]`)
		out, err := runCLI(t, "doctor")
		require.Error(t, err)
		require.Contains(t, out, "lint-config", out)
		require.NotContains(t, out, "\x1b", "raw ESC reached the report")
		require.NotContains(t, out, "\a", "raw BEL reached the report")
	})

	// Not honored: severity is formatted with %v.
	t.Run("unrecognized severity", func(t *testing.T) {
		setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
		appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
			"orphan-notes", `severity = `+hostile)
		out, err := runCLI(t, "doctor")
		require.Error(t, err)
		require.Contains(t, out, "lint-config", out)
		require.NotContains(t, out, "\x1b", "raw ESC reached the report")
		require.NotContains(t, out, "\a", "raw BEL reached the report")

		// The machine-readable surface is safe today (encoding/json escapes
		// control bytes). Pinned so a hand-rolled encoder cannot regress it.
		jsonOut, err := runCLI(t, "doctor", "--json")
		require.Error(t, err)
		require.NotContains(t, jsonOut, "\x1b", "raw ESC in the JSON payload")
	})
}

// Exit codes are the contract cron and CI read. Doctor reports rather than
// aborts, so a broken lint config is a failed CHECK (exit 1, doctor's
// existing contract for any failure) even though `pkms lint` calls the same
// config a config error (exit 2). Pinned because the two numbers differ for
// one underlying fault.
func TestDoctorExitCodeIsOneWhenTheLintConfigIsBroken(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
		"non-markdown-in-note-folders", `scopes = ["[unclosed"]`)

	lintCode, lintErr := executeWithArgs(t, "pkms", "lint")
	require.Equal(t, 2, lintCode, "premise: lint calls this a config error")
	require.Contains(t, lintErr, "[unclosed")

	doctorCode, _ := executeWithArgs(t, "pkms", "doctor")
	require.Equal(t, 1, doctorCode,
		"doctor reports failures with exit 1; 0 would certify a dead linter")
}
