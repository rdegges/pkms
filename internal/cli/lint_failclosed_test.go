package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/profile"
)

// appendVaultLintOverride writes a real [vaults.lint."<rule>"] table into the
// config file. Going through TOML matters: the engine's validators have to
// cope with whatever koanf decodes, not with a hand-built Go map.
func appendVaultLintOverride(t *testing.T, cfgPath, rule, body string) {
	t.Helper()
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	_, err = f.WriteString("\n  [vaults.lint.\"" + rule + "\"]\n  " + body + "\n")
	require.NoError(t, err)
}

// Pins the decoded shape the engine's validators must handle. If a koanf or
// TOML-parser upgrade starts producing []string (or anything else), the
// validator's type switch is the thing that silently stops validating.
func TestVaultLintOverridesDecodeAsAnySlice(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "frontmatter-schema", `warning_types = ["person"]`)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 1)
	raw := cfg.Vaults[0].Lint["frontmatter-schema"]["warning_types"]
	list, ok := raw.([]any)
	require.Truef(t, ok, "vault lint overrides decode as %T; the engine only "+
		"understands []any and []string", raw)
	require.Equal(t, []any{"person"}, list)
}

// End-to-end fail-closed: a malformed junk pattern in the user's config must
// stop the run with an explanatory error — not report a clean vault.
func TestLintMalformedJunkPatternFailsClosedAtTheCLI(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "no-junk-files", `patterns = ["[unclosed"]`)

	out, err := runCLI(t, "lint")
	require.Error(t, err)
	require.NotErrorIs(t, err, errFindings,
		"a broken config is a config error, not a findings exit")
	require.Contains(t, err.Error(), "[unclosed", "the message must name the pattern")
	require.NotContains(t, out, "clean", "a broken config must never print a clean report: %s", out)
}

// The machine-readable surface is the dangerous one: a consumer that sees
// {"findings": []} treats the vault as clean. On a config error nothing may
// be printed at all.
func TestLintJSONPrintsNoPayloadOnAConfigError(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "no-junk-files", `patterns = ["[unclosed"]`)

	out, err := runCLI(t, "lint", "--json")
	require.Error(t, err)
	require.NotErrorIs(t, err, errFindings)
	require.NotContains(t, out, "findings", "no JSON payload may be emitted: %s", out)
	require.False(t, json.Valid([]byte(out)) && strings.TrimSpace(out) != "",
		"stdout must not parse as a lint payload: %s", out)
}

// The same fail-closed contract for the warning_types check.
func TestLintUnknownWarningTypeFailsClosedAtTheCLI(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "frontmatter-schema", `warning_types = ["meetings"]`)

	out, err := runCLI(t, "lint")
	require.Error(t, err)
	require.NotErrorIs(t, err, errFindings)
	require.Contains(t, err.Error(), "meetings")
	require.NotContains(t, out, "clean", out)
}

// Exit codes are the contract cron and CI read. A config error must be
// distinguishable from "findings reported" (1) and from success (0).
func TestLintConfigErrorExitCodeIsNotFindingsOrSuccess(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "no-junk-files", `patterns = ["[unclosed"]`)

	// Execute() is the real exit-code mapper; it reads os.Args and writes the
	// reason to stderr, so both are redirected here.
	stderr := filepath.Join(t.TempDir(), "stderr")
	fh, err := os.Create(stderr)
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = fh
	origArgs := os.Args
	os.Args = []string{"pkms", "lint"}
	code := Execute()
	os.Args = origArgs
	os.Stderr = orig
	require.NoError(t, fh.Close())

	require.Equal(t, 2, code, "config errors exit 2, not 0 (clean) and not 1 (findings)")
	msg, err := os.ReadFile(stderr)
	require.NoError(t, err)
	require.Contains(t, string(msg), "[unclosed", "the user must see why: %s", msg)
}

// --fix writes to the vault. A broken config must stop before any write, so
// no snapshot is taken and no file is touched.
func TestLintFixMakesNoChangesWhenTheConfigIsBroken(t *testing.T) {
	const fixable = "---\nlast_met: 2026/01/02\nmeeting_count: 1\ntopics:\n  - AI\n---\nbody\n"
	vaultDir := setupLintVault(t, map[string]string{"People/Snyk/Fixme.md": fixable})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "no-junk-files", `patterns = ["[unclosed"]`)

	before := snapshotTree(t, vaultDir)
	out, err := runCLI(t, "lint", "--fix")
	require.Error(t, err, out)
	require.NotErrorIs(t, err, errFindings)
	require.Equal(t, before, snapshotTree(t, vaultDir),
		"a config error must abort before any repair is applied")
}

// The production config shape: overrides that carry only a severity key.
// This is what the live vault runs, so the new validation must be a no-op
// for it — and the severity must still take effect.
func TestLintSeverityOnlyOverridesAreUnaffectedByTheNewValidation(t *testing.T) {
	setupLintVault(t, map[string]string{
		// Orphan resource → a warning by default.
		"Resources/Personal/Orphan.md": "---\ntype: resource\n---\nx\n",
		"index.md":                     "# Index\n",
	})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "orphan-notes", `severity = "error"`)

	out, err := runCLI(t, "lint", "--rules", "orphan-notes", "--json")
	require.ErrorIs(t, err, errFindings, "the override must promote the warning: %s", out)

	var payload struct {
		Summary map[string]int `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Positive(t, payload.Summary["error"], "severity override still applies: %s", out)
	require.Zero(t, payload.Summary["warning"], out)
}

// `--rules` is split on commas, so a trailing comma produces an empty id.
// It must fail closed like any other unknown rule, not run a rule set the
// user did not ask for.
func TestLintRejectsUnknownAndEmptyRuleIDs(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	for _, arg := range []string{"empty-note,", "no-such-rule", ",", "empty-note,no-such-rule"} {
		out, err := runCLI(t, "lint", "--rules", arg)
		require.Errorf(t, err, "--rules %q must fail: %s", arg, out)
		require.NotErrorIs(t, err, errFindings, "--rules %q", arg)
		require.Contains(t, err.Error(), "unknown lint rule")
		require.NotContains(t, out, "clean", out)
	}
}

// The MCP lint tool is the agent-facing surface: an agent that receives a
// findings payload treats the vault as checked. A broken config must come
// back as a tool error with the reason, never as a payload.
func TestMCPLintMalformedConfigIsAToolError(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "no-junk-files", `patterns = ["[unclosed"]`)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "lint", map[string]any{"vault": "lintv"})
	require.True(t, isErr, "a broken config must be a tool error, got: %s", got)
	require.Contains(t, got, "[unclosed", "the agent must be told why: %s", got)
	require.NotContains(t, got, `"findings"`, "no payload may be returned: %s", got)
}

// A vault with no lint overrides at all (the default install) must lint on
// every built-in profile: the new validation also reads the profile's own
// [lint.*] tables, so a typo there would break every user of that profile.
func TestFreshVaultLintsOnEveryBuiltinProfile(t *testing.T) {
	for _, prof := range profile.Builtins() {
		t.Run(prof, func(t *testing.T) {
			testEnv(t)
			vaultPath := filepath.Join(t.TempDir(), "v")
			out, err := runCLI(t, "init", "--path", vaultPath, "--profile", prof)
			require.NoError(t, err, out)

			out, err = runCLI(t, "lint", "--vault", "v")
			// Findings are fine; a config error is not.
			if err != nil {
				require.ErrorIs(t, err, errFindings, "profile %q: %s", prof, out)
			}
		})
	}
}

// snapshotTree records every file's relative path and content, excluding
// .git (whose internals churn on read).
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		out[rel] = string(b)
		return nil
	}))
	return out
}
