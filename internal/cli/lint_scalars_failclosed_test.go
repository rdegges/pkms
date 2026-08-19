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

// The scalar/severity/cross-rule checks (#33-#35) had no coverage at the CLI
// boundary, where two things the unit tests cannot see are decided: what the
// TOML decoder hands the validators, and what exit code the user gets.
// docs/LINT-RULES.md promises exit 2 for all three; these hold it to that.

// The decode shape is the load-bearing premise of the whole integer check:
// CfgInt accepts int64 and int and rejects float64, so if koanf ever decodes
// a TOML integer as a float, `warn_at = 60` stops being valid config and
// every vault that sets it breaks. Pinned at the decoder, like
// TestVaultLintOverridesDecodeAsAnySlice does for lists.
func TestVaultLintIntegerOverridesDecodeAsInt64AndStillLint(t *testing.T) {
	setupLintVault(t, map[string]string{
		"Now.md": "# Now\n\n## Today\n- one\n",
	})
	cfgPath := os.Getenv("PKMS_CONFIG")
	appendVaultLintOverride(t, cfgPath, "now-line-cap", "warn_at = 60\n  error_at = 80")

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 1)
	raw := cfg.Vaults[0].Lint["now-line-cap"]["warn_at"]
	_, isInt64 := raw.(int64)
	require.Truef(t, isInt64, "a TOML integer decodes as %T; the engine only "+
		"understands int64 and int, so this shape would reject valid config", raw)

	out, err := runCLI(t, "lint", "--rules", "now-line-cap")
	require.NoError(t, err, "a well-typed integer override must lint clean: %s", out)
}

// A wrong-typed scalar written the way a user writes it — in TOML — must stop
// the run with an explanatory error. The float case is the one the old reader
// silently truncated.
func TestLintWrongTypedScalarFailsClosedAtTheCLI(t *testing.T) {
	for label, body := range map[string]string{
		"quoted integer": `warn_at = "60"`,
		"float":          `warn_at = 59.9`,
		"integer file":   `file = 42`,
		"list file":      `file = ["Now.md"]`,
	} {
		t.Run(label, func(t *testing.T) {
			setupLintVault(t, map[string]string{"Now.md": "# Now\n"})
			appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "now-line-cap", body)

			out, err := runCLI(t, "lint")
			require.Errorf(t, err, "%s must fail the run: %s", label, out)
			require.NotErrorIs(t, err, errFindings,
				"a broken config is a config error, not a findings exit")
			require.Contains(t, err.Error(), "now-line-cap", "the error must name the rule")
			require.NotContains(t, out, "clean",
				"a broken config must never print a clean report: %s", out)
		})
	}
}

// The severity check is the one the live vault is most exposed to: its only
// lint overrides are severity-only, so a typo there is the realistic failure.
// It must be a config error, and it must name the rule the user has to edit.
func TestLintUnrecognizedSeverityFailsClosedAtTheCLI(t *testing.T) {
	for label, body := range map[string]string{
		"wrong case": `severity = "Error"`,
		"plural":     `severity = "warnings"`,
		"empty":      `severity = ""`,
		"integer":    `severity = 1`,
	} {
		t.Run(label, func(t *testing.T) {
			setupLintVault(t, map[string]string{
				"Resources/Personal/Orphan.md": "---\ntype: resource\n---\nx\n",
				"index.md":                     "# Index\n",
			})
			appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", body)

			out, err := runCLI(t, "lint")
			require.Errorf(t, err, "%s must fail the run: %s", label, out)
			require.NotErrorIs(t, err, errFindings)
			require.Contains(t, err.Error(), "severity", "the error must name the key")
			require.Contains(t, err.Error(), "orphan-notes", "the error must name the rule")
			require.NotContains(t, out, "clean", out)
		})
	}
}

// The machine-readable surface: on a severity typo nothing may be printed, or
// a consumer reads {"findings": []} as a checked vault.
func TestLintJSONPrintsNoPayloadOnASeverityTypo(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", `severity = "Error"`)

	out, err := runCLI(t, "lint", "--json")
	require.Error(t, err)
	require.NotErrorIs(t, err, errFindings)
	require.NotContains(t, out, "findings", "no JSON payload may be emitted: %s", out)
	require.False(t, json.Valid([]byte(out)) && strings.TrimSpace(out) != "",
		"stdout must not parse as a lint payload: %s", out)
}

// The agent-facing surface. An agent that gets a findings payload treats the
// vault as checked, so a severity typo must come back as a tool error with
// the reason — the same contract the glob checks already hold for MCP.
func TestMCPLintSeverityTypoIsAToolError(t *testing.T) {
	setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", `severity = "Error"`)

	cs, ctx := connectMCP(t)
	got, isErr := callText(t, cs, ctx, "lint", map[string]any{"vault": "lintv"})
	require.True(t, isErr, "a broken severity must be a tool error, got: %s", got)
	require.Contains(t, got, "severity", "the agent must be told why: %s", got)
	require.NotContains(t, got, `"findings"`, "no payload may be returned: %s", got)
}

// docs/LINT-RULES.md says these shapes exit 2. Verified through Execute(),
// the real exit-code mapper, for the severity and scalar checks alike.
func TestLintScalarAndSeverityConfigErrorsExitTwo(t *testing.T) {
	for label, override := range map[string][2]string{
		"severity typo": {"orphan-notes", `severity = "Error"`},
		"scalar type":   {"now-line-cap", `warn_at = "60"`},
	} {
		t.Run(label, func(t *testing.T) {
			setupLintVault(t, map[string]string{"Areas/Personal/note.md": "x\n"})
			appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), override[0], override[1])

			stderr := filepath.Join(t.TempDir(), "stderr")
			fh, err := os.Create(stderr)
			require.NoError(t, err)
			origErr, origArgs := os.Stderr, os.Args
			os.Stderr = fh
			os.Args = []string{"pkms", "lint"}
			code := Execute()
			os.Args = origArgs
			os.Stderr = origErr
			require.NoError(t, fh.Close())

			require.Equal(t, 2, code,
				"config errors exit 2, not 0 (clean) and not 1 (findings)")
			msg, err := os.ReadFile(stderr)
			require.NoError(t, err)
			require.Contains(t, string(msg), override[0],
				"the user must be told which rule to fix: %s", msg)
		})
	}
}

// --fix writes to the vault, so a severity typo must abort before any repair
// or snapshot — the same contract the glob checks already hold.
func TestLintFixMakesNoChangesOnASeverityTypo(t *testing.T) {
	const fixable = "---\nlast_met: 2026/01/02\nmeeting_count: 1\ntopics:\n  - AI\n---\nbody\n"
	vaultDir := setupLintVault(t, map[string]string{"People/Snyk/Fixme.md": fixable})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "orphan-notes", `severity = "Error"`)

	before := snapshotTree(t, vaultDir)
	out, err := runCLI(t, "lint", "--fix")
	require.Error(t, err, out)
	require.NotErrorIs(t, err, errFindings)
	require.Equal(t, before, snapshotTree(t, vaultDir),
		"a config error must abort before any repair is applied")
}

// The cross-rule read (#35) at the CLI: a vault override on
// root-canonical-only.files is what root-file-name-case checks against, so a
// broken list there must fail even when the user lints only the reader.
func TestLintPeerConfigFailsClosedAtTheCLI(t *testing.T) {
	setupLintVault(t, map[string]string{"now.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "root-canonical-only", `files = "Now.md"`)

	out, err := runCLI(t, "lint", "--rules", "root-file-name-case")
	require.Error(t, err, out)
	require.NotErrorIs(t, err, errFindings)
	require.Contains(t, err.Error(), "root-canonical-only",
		"the error must name the table that holds the bad value: %s", err)
	require.NotContains(t, out, "clean", out)
}

// And the same override, well-formed, must reach the reader through the
// config file — the merge path the unit test exercises with a Go map.
func TestLintPeerConfigOverrideReachesTheReaderAtTheCLI(t *testing.T) {
	setupLintVault(t, map[string]string{"readme.md": "x\n"})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"), "root-canonical-only",
		`files = ["README.md"]`)

	out, err := runCLI(t, "lint", "--rules", "root-file-name-case", "--json")
	require.ErrorIs(t, err, errFindings, "the case variant must be reported: %s", out)

	var payload struct {
		Findings []struct {
			Rule string `json:"rule"`
			Path string `json:"path"`
		} `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Len(t, payload.Findings, 1, out)
	require.Equal(t, "root-file-name-case", payload.Findings[0].Rule)
	require.Equal(t, "readme.md", payload.Findings[0].Path)
}
