package paths

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnvOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-config")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	t.Setenv("XDG_DATA_HOME", "/tmp/xdg-data")
	t.Setenv("PKMS_CONFIG", "")

	require.Equal(t, "/tmp/xdg-config", ConfigHome())
	require.Equal(t, filepath.Join("/tmp/xdg-config", "pkms", "config.toml"), ConfigFile())
	require.Equal(t, filepath.Join("/tmp/xdg-state", "pkms", "state", "v"), StateDir("state", "v"))
	require.Equal(t, filepath.Join("/tmp/xdg-data", "pkms", "assets"), DataDir("assets"))

	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache")
	require.Equal(t, "/tmp/xdg-cache", CacheHome())
	require.Equal(t, filepath.Join("/tmp/xdg-cache", "pkms", "wazero"), CacheDir("wazero"))
}

func TestFallbacksIgnoreRelativeEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/not-allowed") // spec: relative paths are ignored
	// The cache base is the one where honoring a relative value would write
	// INTO the working directory — for pkms that can be the vault itself,
	// and what lands there is ~19 MB of compiled machine code (§31.14).
	t.Setenv("XDG_CACHE_HOME", "relative/not-allowed")
	t.Setenv("HOME", "/home/u")

	require.Equal(t, filepath.Join("/home/u", ".config"), ConfigHome())
	require.Equal(t, filepath.Join("/home/u", ".cache"), CacheHome())
}

func TestDefaultsAreDotDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/home/u")

	require.Equal(t, filepath.Join("/home/u", ".cache"), CacheHome())
	require.Equal(t, filepath.Join("/home/u", ".config"), ConfigHome())
	require.Equal(t, filepath.Join("/home/u", ".local", "state"), StateHome())
	require.Equal(t, filepath.Join("/home/u", ".local", "share"), DataHome())
}

func TestPkmsConfigOverride(t *testing.T) {
	t.Setenv("PKMS_CONFIG", "/etc/pkms/alt.toml")
	require.Equal(t, "/etc/pkms/alt.toml", ConfigFile())
}
