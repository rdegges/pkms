package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/config"
)

// testEnv isolates config + state in temp dirs.
func testEnv(t *testing.T) (configPath string) {
	t.Helper()
	tmp := t.TempDir()
	configPath = filepath.Join(tmp, "config.toml")
	t.Setenv("PKMS_CONFIG", configPath)
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	return configPath
}

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	err := root.Execute()
	return buf.String(), err
}

func TestInitScaffoldsParaVault(t *testing.T) {
	cfgPath := testEnv(t)
	vault := filepath.Join(t.TempDir(), "My Vault")

	out, err := runCLI(t, "init", "--path", vault)
	require.NoError(t, err, out)

	for _, dir := range []string{"Projects", "Areas", "Resources", "Archive", ".git"} {
		st, err := os.Stat(filepath.Join(vault, dir))
		require.NoError(t, err, dir)
		require.True(t, st.IsDir())
	}
	gi, err := os.ReadFile(filepath.Join(vault, ".gitignore"))
	require.NoError(t, err)
	require.Contains(t, string(gi), ".obsidian/workspace*")

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 1)
	require.Equal(t, "my-vault", cfg.Vaults[0].Name, "name slugified from basename")
	require.Equal(t, "para", cfg.Vaults[0].Profile)

	// Idempotent: run again, nothing breaks, no duplicate registration.
	out, err = runCLI(t, "init", "--path", vault)
	require.NoError(t, err, out)
	cfg, _ = config.Load(cfgPath)
	require.Len(t, cfg.Vaults, 1)

	// Doctor is happy (warns at most; no failures).
	out, err = runCLI(t, "doctor", "--json")
	require.NoError(t, err, out)
	var report struct {
		Checks []checkResult `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	for _, c := range report.Checks {
		require.NotEqual(t, "fail", c.Status, "%+v", c)
	}
}

func TestInitRefusesNonEmptyWithoutAdopt(t *testing.T) {
	testEnv(t)
	vault := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(vault, "existing.md"), []byte("x"), 0o644))

	_, err := runCLI(t, "init", "--path", vault)
	require.ErrorContains(t, err, "--adopt")

	out, err := runCLI(t, "init", "--path", vault, "--adopt", "--name", "adopted")
	require.NoError(t, err, out)

	// Adopt never scaffolds folders over existing content.
	_, statErr := os.Stat(filepath.Join(vault, "Projects"))
	require.True(t, os.IsNotExist(statErr), "adopt must not scaffold")
	// But the content is preserved and the repo exists.
	_, err = os.Stat(filepath.Join(vault, "existing.md"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(vault, ".git"))
	require.NoError(t, err)
}

func TestInitDryRunChangesNothing(t *testing.T) {
	cfgPath := testEnv(t)
	vault := filepath.Join(t.TempDir(), "v")

	out, err := runCLI(t, "init", "--path", vault, "--dry-run")
	require.NoError(t, err)
	require.Contains(t, out, "plan")

	_, statErr := os.Stat(vault)
	require.True(t, os.IsNotExist(statErr))
	_, statErr = os.Stat(cfgPath)
	require.True(t, os.IsNotExist(statErr), "config untouched on dry-run")
}

func TestInitRejectsNameCollision(t *testing.T) {
	testEnv(t)
	v1 := filepath.Join(t.TempDir(), "vault")
	v2 := filepath.Join(t.TempDir(), "vault")

	_, err := runCLI(t, "init", "--path", v1)
	require.NoError(t, err)
	_, err = runCLI(t, "init", "--path", v2)
	require.ErrorContains(t, err, "already registered")
}

func TestDoctorFailsOnMissingVaultPath(t *testing.T) {
	cfgPath := testEnv(t)
	require.NoError(t, config.AppendVault(cfgPath, config.Vault{
		Name: "ghost", Path: "/nonexistent/vault", Profile: "para",
	}))
	out, err := runCLI(t, "doctor")
	require.ErrorIs(t, err, errFindings, out)
}

func TestProfileListAndEject(t *testing.T) {
	testEnv(t)
	out, err := runCLI(t, "profile", "list")
	require.NoError(t, err)
	require.Contains(t, out, "para")
	require.Contains(t, out, "rdegges")

	dest := filepath.Join(t.TempDir(), "ejected")
	require.NoError(t, os.MkdirAll(dest, 0o755))
	_, err = runCLI(t, "profile", "eject", "rdegges", dest)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dest, "schemas", "person.schema.json"))
	require.NoError(t, err)
}
