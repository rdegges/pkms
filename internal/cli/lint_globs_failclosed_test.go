package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
)

// The #29 gate got end-to-end CLI proofs for junk patterns and warning_types.
// #30 (globs) and #31 (list shapes) added two new rejection classes; these
// walk them through the same real surfaces — a TOML config file, the exit
// code, the JSON payload, the fix path, and a custom profile on disk.

// A malformed scope glob written in the user's real config must stop the run
// with an explanatory error, not report a clean vault.
func TestLintMalformedScopeGlobFailsClosedAtTheCLI(t *testing.T) {
	setupLintVault(t, map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
	})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "non-markdown-in-note-folders", `scopes = ["[unclosed"]`)

	out, err := runCLI(t, "lint")
	require.Error(t, err, out)
	require.NotErrorIs(t, err, errFindings,
		"a broken glob is a config error, not a findings exit")
	require.Contains(t, err.Error(), "[unclosed", "the message must name the pattern")
	require.NotContains(t, out, "clean", "a broken config must never print a clean report: %s", out)
}

// A list-valued option written as a bare scalar in real TOML must fail too:
// this is the shape that used to silently DISABLE the rule.
func TestLintScalarForListConfigFailsClosedAtTheCLI(t *testing.T) {
	for rule, body := range map[string]string{
		"non-markdown-in-note-folders": `scopes = "Areas/**"`,
		"no-junk-files":                `patterns = "*.bak"`,
		"frontmatter-schema":           `warning_types = "person"`,
		"person-required-sections":     `sections = "About"`,
	} {
		t.Run(rule, func(t *testing.T) {
			setupLintVault(t, map[string]string{
				"Areas/Personal/note.md":  "x\n",
				"Areas/Personal/junk.txt": "x\n",
				"Areas/Personal/old.bak":  "x\n",
			})
			appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), rule, body)

			out, err := runCLI(t, "lint")
			require.Errorf(t, err, "%s must reject a scalar: %s", rule, out)
			require.NotErrorIs(t, err, errFindings, "%s: %s", rule, out)
			require.Contains(t, err.Error(), rule, "the message must name the rule")
			require.NotContains(t, out, "clean", out)
		})
	}
}

// The machine-readable surface is the dangerous one: a consumer that reads
// {"findings": []} treats the vault as checked and clean.
func TestLintJSONPrintsNoPayloadOnAMalformedGlob(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", `scopes = ["[unclosed"]`)

	out, err := runCLI(t, "lint", "--json")
	require.Error(t, err)
	require.NotErrorIs(t, err, errFindings)
	require.NotContains(t, out, "findings", "no JSON payload may be emitted: %s", out)
	require.False(t, json.Valid([]byte(out)) && strings.TrimSpace(out) != "",
		"stdout must not parse as a lint payload: %s", out)
}

// Exit codes are the contract cron and CI read: a config error must be
// distinguishable from findings (1) and from success (0).
func TestLintMalformedGlobExitCodeIsTwo(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
		"non-markdown-in-note-folders", `scopes = ["[unclosed"]`)

	code, stderr := executeWithArgs(t, "pkms", "lint")
	require.Equal(t, 2, code, "config errors exit 2, not 0 (clean) and not 1 (findings)")
	require.Contains(t, stderr, "[unclosed", "the user must see why: %s", stderr)
}

// --fix writes to the vault. A broken glob must stop before any write.
func TestLintFixMakesNoChangesWhenAGlobIsMalformed(t *testing.T) {
	const fixable = "---\nlast_met: 2026/01/02\nmeeting_count: 1\ntopics:\n  - AI\n---\nbody\n"
	vaultDir := setupLintVault(t, map[string]string{"People/Snyk/Fixme.md": fixable})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", `scopes = ["[unclosed"]`)

	before := snapshotTree(t, vaultDir)
	out, err := runCLI(t, "lint", "--fix")
	require.Error(t, err, out)
	require.NotErrorIs(t, err, errFindings)
	require.Equal(t, before, snapshotTree(t, vaultDir),
		"a config error must abort before any repair is applied")
}

// The MCP lint tool is the agent-facing surface: an agent that receives a
// findings payload treats the vault as checked.
func TestMCPLintMalformedGlobIsAToolError(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
		"non-markdown-in-note-folders", `scopes = ["[unclosed"]`)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "lint", map[string]any{"vault": "lintv"})
	require.True(t, isErr, "a broken glob must be a tool error, got: %s", got)
	require.Contains(t, got, "[unclosed", "the agent must be told why: %s", got)
	require.NotContains(t, got, `"findings"`, "no payload may be returned: %s", got)
}

// ---- custom profiles on disk ---------------------------------------------

// Profile type scopes are now validated at load, which is the gate for every
// command that touches a vault. A hand-edited or ejected profile with a
// malformed scope must be rejected by all of them — a profile that loads and
// classifies nothing would misfile every note of that type in silence.
func TestCustomProfileWithMalformedScopeGlobIsRejectedEverywhere(t *testing.T) {
	cfgPath := testEnv(t)
	profDir := filepath.Join(t.TempDir(), "prof")
	require.NoError(t, os.MkdirAll(profDir, 0o755))
	manifest := `schema_version = 1
name = "custom"
scaffold = ["Areas"]

[[types]]
name = "area"
scope = ["Areas/**", "Areas/[unclosed"]
`
	require.NoError(t, os.WriteFile(filepath.Join(profDir, "profile.toml"), []byte(manifest), 0o644))

	vaultDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "Areas"), 0o755))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "customv", Path: vaultDir, Profile: profDir,
	}))

	for _, args := range [][]string{{"lint"}, {"lint", "--json"}, {"profile", "show"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runCLI(t, args...)
			require.Errorf(t, err, "%v must reject the profile: %s", args, out)
			require.NotErrorIs(t, err, errFindings, "%v: %s", args, out)
			require.Contains(t, err.Error(), "[unclosed",
				"the message must name the offending pattern")
			require.NotContains(t, out, "clean", out)
		})
	}

	// doctor reports rather than aborts, so its contract is different: the
	// profile check must be a FAILURE line naming the pattern, and the run
	// must still exit non-zero.
	t.Run("doctor", func(t *testing.T) {
		out, err := runCLI(t, "doctor")
		require.Error(t, err, out)
		require.Contains(t, out, "customv: profile", out)
		require.Contains(t, out, "[unclosed", "doctor must name the pattern: %s", out)
		require.Contains(t, out, "1 failures", "the bad profile must count as a failure: %s", out)
	})

	// And the same profile with the bad glob removed works — proving the
	// rejection above is the glob gate, not some unrelated profile error.
	good := strings.Replace(manifest, `, "Areas/[unclosed"`, "", 1)
	require.NoError(t, os.WriteFile(filepath.Join(profDir, "profile.toml"), []byte(good), 0o644))
	out, err := runCLI(t, "lint")
	if err != nil {
		require.ErrorIs(t, err, errFindings, "findings are fine, config errors are not: %s", out)
	}
}

// executeWithArgs drives the real exit-code mapper (Execute reads os.Args and
// writes the reason to stderr) and returns the code plus captured stderr.
func executeWithArgs(t *testing.T, args ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	fh, err := os.Create(path)
	require.NoError(t, err)
	origErr, origArgs := os.Stderr, os.Args
	os.Stderr, os.Args = fh, args
	code := Execute()
	os.Stderr, os.Args = origErr, origArgs
	require.NoError(t, fh.Close())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return code, string(b)
}
