// Package paths resolves pkms' XDG base directories.
//
// It follows the XDG Base Directory spec literally on every Unix platform,
// including macOS: $XDG_*_HOME when set, otherwise ~/.config, ~/.local/state
// and ~/.local/share. (adrg/xdg was rejected because it diverges to
// ~/Library/Application Support on macOS, which breaks the documented
// $XDG_CONFIG_HOME/pkms/config.toml contract.)
package paths

import (
	"os"
	"path/filepath"
)

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func baseDir(envVar, fallback string) string {
	if v := os.Getenv(envVar); v != "" && filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(home(), fallback)
}

// ConfigHome returns the XDG config base (no pkms suffix).
func ConfigHome() string { return baseDir("XDG_CONFIG_HOME", ".config") }

// StateHome returns the XDG state base (no pkms suffix).
func StateHome() string { return baseDir("XDG_STATE_HOME", filepath.Join(".local", "state")) }

// DataHome returns the XDG data base (no pkms suffix).
func DataHome() string { return baseDir("XDG_DATA_HOME", filepath.Join(".local", "share")) }

// CacheHome returns the XDG cache base (no pkms suffix). Cache contents
// are machine-local and disposable: deleting them costs recomputation,
// never correctness (SPEC §31.14).
func CacheHome() string { return baseDir("XDG_CACHE_HOME", ".cache") }

// ConfigFile returns the pkms config path. PKMS_CONFIG overrides everything.
func ConfigFile() string {
	if v := os.Getenv("PKMS_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(ConfigHome(), "pkms", "config.toml")
}

// StateDir returns $XDG_STATE_HOME/pkms/<parts...>.
func StateDir(parts ...string) string {
	return filepath.Join(append([]string{StateHome(), "pkms"}, parts...)...)
}

// DataDir returns $XDG_DATA_HOME/pkms/<parts...>.
func DataDir(parts ...string) string {
	return filepath.Join(append([]string{DataHome(), "pkms"}, parts...)...)
}

// CacheDir returns $XDG_CACHE_HOME/pkms/<parts...>.
func CacheDir(parts ...string) string {
	return filepath.Join(append([]string{CacheHome(), "pkms"}, parts...)...)
}

// ProfilesDir returns the user-profile directory root.
func ProfilesDir() string {
	return filepath.Join(ConfigHome(), "pkms", "profiles")
}
