package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	return p
}

func TestLoadValid(t *testing.T) {
	p := writeConfig(t, `
version = 1

[defaults]
profile = "para"

[[vaults]]
name = "personal"
path = "/vaults/personal"

[[vaults]]
name = "work"
path = "/vaults/work"
profile = "rdegges"

  [vaults.snapshot]
  remote = "git@example.com:me/snaps.git"
`)
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 2)
	require.Equal(t, "para", cfg.Vaults[0].Profile, "defaults.profile fills the gap")
	require.Equal(t, "rdegges", cfg.Vaults[1].Profile)
	require.Equal(t, "git@example.com:me/snaps.git", cfg.Vaults[1].Snapshot.Remote)
}

func TestLoadMissingFileIsEmptyConfig(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	require.NoError(t, err)
	require.Empty(t, cfg.Vaults)
	require.Equal(t, SupportedVersion, cfg.Version)
}

func TestLoadRejectsUnsupportedVersion(t *testing.T) {
	p := writeConfig(t, "version = 99\n")
	_, err := Load(p)
	require.ErrorContains(t, err, "version 99")
}

func TestLoadRejectsDuplicateNames(t *testing.T) {
	p := writeConfig(t, `
version = 1
[[vaults]]
name = "a"
path = "/x"
profile = "para"
[[vaults]]
name = "a"
path = "/y"
profile = "para"
`)
	_, err := Load(p)
	require.ErrorContains(t, err, "duplicate vault name")
}

func TestLoadRejectsBadName(t *testing.T) {
	p := writeConfig(t, `
version = 1
[[vaults]]
name = "Bad Name"
path = "/x"
profile = "para"
`)
	_, err := Load(p)
	require.ErrorContains(t, err, "must match")
}

func TestLoadRejectsMissingProfile(t *testing.T) {
	p := writeConfig(t, `
version = 1
[[vaults]]
name = "a"
path = "/x"
`)
	_, err := Load(p)
	require.ErrorContains(t, err, "no profile")
}

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	got, err := ExpandPath("~/Vault")
	require.NoError(t, err)
	require.Equal(t, "/home/u/Vault", got)

	_, err = ExpandPath("relative/path")
	require.ErrorContains(t, err, "absolute")

	_, err = ExpandPath("")
	require.Error(t, err)
}

func TestVaultSelection(t *testing.T) {
	cfg := &Config{Vaults: []Vault{{Name: "a"}, {Name: "b"}}}

	_, err := cfg.Vault("")
	require.ErrorContains(t, err, "--vault")

	v, err := cfg.Vault("b")
	require.NoError(t, err)
	require.Equal(t, "b", v.Name)

	_, err = cfg.Vault("zzz")
	require.ErrorContains(t, err, "unknown vault")

	one := &Config{Vaults: []Vault{{Name: "solo"}}}
	v, err = one.Vault("")
	require.NoError(t, err)
	require.Equal(t, "solo", v.Name)

	none := &Config{}
	_, err = none.Vault("")
	require.ErrorContains(t, err, "pkms init")
}

func TestAppendVaultCreatesAndAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")

	require.NoError(t, AppendVault(p, Vault{Name: "one", Path: "/v/one", Profile: "para"}))
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 1)

	// Append preserves existing content (incl. comments).
	raw, _ := os.ReadFile(p)
	commented := append([]byte("# my comment\n"), raw...)
	require.NoError(t, os.WriteFile(p, commented, 0o644))

	require.NoError(t, AppendVault(p, Vault{
		Name: "two", Path: "/v/two", Profile: "rdegges",
		Snapshot: Snapshot{Remote: "git@example.com:me/s.git"},
	}))
	cfg, err = Load(p)
	require.NoError(t, err)
	require.Len(t, cfg.Vaults, 2)
	require.Equal(t, "git@example.com:me/s.git", cfg.Vaults[1].Snapshot.Remote)

	raw, _ = os.ReadFile(p)
	require.Contains(t, string(raw), "# my comment")
}
