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
}

func TestFallbacksIgnoreRelativeEnv(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/not-allowed") // spec: relative paths are ignored
	t.Setenv("HOME", "/home/u")

	require.Equal(t, filepath.Join("/home/u", ".config"), ConfigHome())
}

func TestDefaultsAreDotDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/u")

	require.Equal(t, filepath.Join("/home/u", ".config"), ConfigHome())
	require.Equal(t, filepath.Join("/home/u", ".local", "state"), StateHome())
	require.Equal(t, filepath.Join("/home/u", ".local", "share"), DataHome())
}

func TestPkmsConfigOverride(t *testing.T) {
	t.Setenv("PKMS_CONFIG", "/etc/pkms/alt.toml")
	require.Equal(t, "/etc/pkms/alt.toml", ConfigFile())
}
