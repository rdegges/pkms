package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
)

// #30's final shape at the CLI: syntax is validated when config and profiles
// load ("[unclosed" is covered by the tests in lint_globs_failclosed_test.go),
// and every accepted glob evaluates — classification and scope matching use
// doublestar.MatchUnvalidated, so a divergent-but-valid pattern ("{[}]}"
// passes ValidatePattern, Match refuses it on its suffix re-validation) can
// neither abort a run nor brick a vault.
const matchTimeGlob = "{[}]}"

// A divergent scope glob in the user's real config evaluates: lint completes
// with a config-independent verdict (exit 0 or a findings exit, never a
// config error).
func TestLintEvaluatesADivergentScopeGlobAtTheCLI(t *testing.T) {
	require.True(t, doublestar.ValidatePattern(matchTimeGlob))
	setupLintVault(t, map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
	})
	appendVaultLintOverride(t, os.Getenv("PKMS_CONFIG"),
		"non-markdown-in-note-folders", `scopes = ["`+matchTimeGlob+`"]`)

	out, err := runCLI(t, "lint")
	if err != nil {
		require.ErrorIs(t, err, errFindings,
			"an accepted glob must evaluate — findings are fine, a config error is not: %s", out)
	}
}

// setupMatchTimeScopeVault registers a vault on a custom profile whose only
// type scope is the divergent glob. The profile loads (syntax is fine) and
// must classify deterministically everywhere.
func setupMatchTimeScopeVault(t *testing.T) string {
	t.Helper()
	cfgPath := testEnv(t)
	profDir := filepath.Join(t.TempDir(), "prof")
	require.NoError(t, os.MkdirAll(profDir, 0o755))
	manifest := "schema_version = 1\nname = \"divergent\"\nscaffold = [\"Notes\"]\n\n" +
		"[[types]]\nname = \"note\"\nscope = [\"" + matchTimeGlob + "\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(profDir, "profile.toml"), []byte(manifest), 0o644))

	vaultDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "Notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(vaultDir, "Notes", "A.md"),
		[]byte("---\ntitle: x\n---\nbody\n"), 0o644))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "dv", Path: vaultDir, Profile: profDir,
	}))
	return vaultDir
}

// `query --type` under a divergent profile scope answers deterministically —
// exactly what MatchUnvalidated says — and a query without a type filter
// still lists the vault. A divergent scope must never brick the CLI.
func TestQueryClassifiesUnderADivergentProfileScope(t *testing.T) {
	setupMatchTimeScopeVault(t)

	out, err := runCLI(t, "query", "--vault", "dv", "--type", "note")
	require.NoError(t, err, out)
	if doublestar.MatchUnvalidated(matchTimeGlob, "Notes/A.md") {
		require.Contains(t, out, "Notes/A.md", out)
	} else {
		require.NotContains(t, out, "Notes/A.md", out)
	}

	out, err = runCLI(t, "query", "--vault", "dv")
	require.NoError(t, err, out)
	require.Contains(t, out, "Notes/A.md", out)
}
