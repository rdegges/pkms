// Package config loads and validates the pkms TOML config
// ($XDG_CONFIG_HOME/pkms/config.toml; PKMS_CONFIG overrides).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	toml "github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"

	"github.com/rdegges/pkms/internal/paths"
)

// SupportedVersion is the config schema version this binary reads.
const SupportedVersion = 1

var vaultNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Config struct {
	Version  int      `koanf:"version"`
	Defaults Defaults `koanf:"defaults"`
	Vaults   []Vault  `koanf:"vaults"`

	// Path the config was loaded from (not part of the file).
	Path string `koanf:"-"`
}

type Defaults struct {
	Profile string `koanf:"profile"`
}

type Vault struct {
	Name     string   `koanf:"name"`
	Path     string   `koanf:"path"`
	Profile  string   `koanf:"profile"`
	Snapshot Snapshot `koanf:"snapshot"`
	// Per-vault lint overrides: rule id -> options ("enabled", "severity", ...).
	Lint map[string]map[string]any `koanf:"lint"`
}

type Snapshot struct {
	Remote string `koanf:"remote"`
}

// Load reads the config at path (or the default location when path is empty).
// A missing file yields an empty valid config so `pkms init` can create it.
func Load(path string) (*Config, error) {
	if path == "" {
		path = paths.ConfigFile()
	}
	cfg := &Config{Version: SupportedVersion, Path: path}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	k := koanf.New(".")
	if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.Path = path

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.Version != SupportedVersion {
		return fmt.Errorf("version %d is not supported by this pkms (wants %d); upgrade pkms", c.Version, SupportedVersion)
	}
	seen := map[string]bool{}
	for i := range c.Vaults {
		v := &c.Vaults[i]
		if !vaultNameRe.MatchString(v.Name) {
			return fmt.Errorf("vault %d: name %q must match %s", i, v.Name, vaultNameRe)
		}
		if seen[v.Name] {
			return fmt.Errorf("duplicate vault name %q", v.Name)
		}
		seen[v.Name] = true

		p, err := ExpandPath(v.Path)
		if err != nil {
			return fmt.Errorf("vault %q: %w", v.Name, err)
		}
		v.Path = p

		if v.Profile == "" {
			v.Profile = c.Defaults.Profile
		}
		if v.Profile == "" {
			return fmt.Errorf("vault %q: no profile set and no [defaults].profile", v.Name)
		}
	}
	return nil
}

// Vault returns the vault named name, or the only vault when name is empty.
func (c *Config) Vault(name string) (*Vault, error) {
	if name == "" {
		switch len(c.Vaults) {
		case 0:
			return nil, fmt.Errorf("no vaults configured; run `pkms init` first")
		case 1:
			return &c.Vaults[0], nil
		default:
			return nil, fmt.Errorf("multiple vaults configured; pick one with --vault")
		}
	}
	for i := range c.Vaults {
		if c.Vaults[i].Name == name {
			return &c.Vaults[i], nil
		}
	}
	return nil, fmt.Errorf("unknown vault %q", name)
}

// ExpandPath expands a leading ~ and requires the result to be absolute.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be absolute", p)
	}
	return filepath.Clean(p), nil
}

// AppendVault appends a [[vaults]] entry to the config file, creating the
// file (with its version header) when missing. Appending — rather than
// re-marshalling — preserves user comments and formatting.
func AppendVault(path string, v Vault) error {
	if path == "" {
		path = paths.ConfigFile()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf strings.Builder
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		fmt.Fprintf(&buf, "version = %d\n", SupportedVersion)
	case err != nil:
		return err
	default:
		buf.Write(existing)
		if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
			buf.WriteString("\n")
		}
	}
	buf.WriteString("\n[[vaults]]\n")
	fmt.Fprintf(&buf, "name    = %q\n", v.Name)
	fmt.Fprintf(&buf, "path    = %q\n", v.Path)
	fmt.Fprintf(&buf, "profile = %q\n", v.Profile)
	if v.Snapshot.Remote != "" {
		fmt.Fprintf(&buf, "\n  [vaults.snapshot]\n  remote = %q\n", v.Snapshot.Remote)
	}
	return os.WriteFile(path, []byte(buf.String()), 0o644)
}
