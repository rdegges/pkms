package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/lint"
)

// setupLintVault registers a git-backed vault on the rdegges profile.
func setupLintVault(t *testing.T, files map[string]string) string {
	t.Helper()
	cfgPath := testEnv(t)
	vaultDir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(vaultDir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	out, err := exec.Command("git", "-C", vaultDir, "init", "-q").CombinedOutput()
	require.NoError(t, err, string(out))
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "lintv", Path: vaultDir, Profile: "rdegges",
	}))
	// Ops need at least one commit.
	_, err = runCLI(t, "snapshot")
	require.NoError(t, err)
	return vaultDir
}

func TestLintExitCodesAndJSON(t *testing.T) {
	setupLintVault(t, map[string]string{
		"People/Snyk/Broken.md": "# no frontmatter\n",
	})

	out, err := runCLI(t, "lint", "--json")
	require.ErrorIs(t, err, errFindings, "errors → exit 1 sentinel")

	var payload struct {
		Vault    string         `json:"vault"`
		Findings []lint.Finding `json:"findings"`
		Summary  map[string]int `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	require.Equal(t, "lintv", payload.Vault)
	require.NotEmpty(t, payload.Findings)
	require.Positive(t, payload.Summary["error"])
}

func TestLintCleanExitsZero(t *testing.T) {
	setupLintVault(t, map[string]string{
		"Areas/Personal/note.md": "Just a note.\n",
	})
	out, err := runCLI(t, "lint")
	require.NoError(t, err, out)
	require.Contains(t, out, "clean")
}

func TestLintFailOnWarning(t *testing.T) {
	setupLintVault(t, map[string]string{
		// Orphan resource → warning only.
		"Resources/Personal/Orphan.md": "---\ntype: resource\n---\nx\n",
		"index.md":                     "# Index\n",
	})
	_, err := runCLI(t, "lint")
	require.NoError(t, err, "warnings don't fail at default fail-on=error")
	_, err = runCLI(t, "lint", "--fail-on", "warning")
	require.ErrorIs(t, err, errFindings)
}

func TestLintFixAppliesAndCommits(t *testing.T) {
	vaultDir := setupLintVault(t, map[string]string{
		"People/Snyk/Fixme.md": `---
last_met: 2026/01/02
meeting_count: 1
topics:
  - AI Security
---
## Meta
- **Last Updated**: 2026-01-02

## Relationship
r

## Meeting History
`,
	})

	out, err := runCLI(t, "lint", "--fix")
	require.ErrorIs(t, err, errFindings, "unfixable orphan/section findings may remain; fix output: %s", out)
	require.Contains(t, out, "applied")

	fixed, err := os.ReadFile(filepath.Join(vaultDir, "People/Snyk/Fixme.md"))
	require.NoError(t, err)
	require.Contains(t, string(fixed), "last_met: 2026-01-02")
	require.Contains(t, string(fixed), "- ai-security")

	// The fix committed its own diff. (No pre() commit here: the worktree
	// was clean when the op began, so HEAD is already the isolation point.)
	log, err := exec.Command("git", "-C", vaultDir, "log", "--format=%s").Output()
	require.NoError(t, err)
	require.Contains(t, string(log), "lint-fix:")

	// Fix twice = no-op: second run applies nothing.
	out2, _ := runCLI(t, "lint", "--fix")
	require.NotContains(t, out2, "applied", "second --fix run must be a no-op")

	// And undo reverts the fix.
	_, err = runCLI(t, "undo", "last")
	require.NoError(t, err)
	reverted, _ := os.ReadFile(filepath.Join(vaultDir, "People/Snyk/Fixme.md"))
	require.Contains(t, string(reverted), "last_met: 2026/01/02")
}

func TestLintRulesFilter(t *testing.T) {
	setupLintVault(t, map[string]string{
		"People/Snyk/Broken.md": "# no frontmatter\n",
		"Rogue.md":              "x\n",
	})
	out, err := runCLI(t, "lint", "--rules", "root-canonical-only", "--json")
	require.ErrorIs(t, err, errFindings)
	var payload struct {
		Findings []lint.Finding `json:"findings"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &payload))
	for _, f := range payload.Findings {
		require.Equal(t, "root-canonical-only", f.Rule)
	}
	require.True(t, strings.Contains(out, "Rogue.md"))
}
