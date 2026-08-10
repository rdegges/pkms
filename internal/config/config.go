// Package config loads and validates the pkms TOML config
// ($XDG_CONFIG_HOME/pkms/config.toml; PKMS_CONFIG overrides).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// Assets holds the [vaults.assets] storage policy knobs (SPEC §31.2).
	Assets Assets `koanf:"assets"`
	// Per-vault lint overrides: rule id -> options ("enabled", "severity", ...).
	Lint map[string]map[string]any `koanf:"lint"`
	// Raw [[vaults.ingesters]] tables; validated into Sources.
	Ingesters []map[string]any `koanf:"ingesters"`

	// Sources is the validated form of Ingesters (not read from TOML).
	Sources []IngesterConfig `koanf:"-"`
}

// IngesterConfig is one validated [[vaults.ingesters]] entry (SPEC §18).
// Type, name, enabled and timeout are pipeline-reserved keys; everything
// else stays in Options for the ingester factory, which must reject keys
// it does not know (typo protection).
type IngesterConfig struct {
	Type    string
	Name    string
	Enabled bool
	Timeout time.Duration
	Options map[string]any
}

// Source is the state/lock identity, e.g. "imap:fastmail" (SPEC §18).
// The push pipeline's synthetic adhoc source collapses to plain "adhoc";
// every real configured source keeps its <type>:<name> form even when a
// user happens to name it the same as its type.
func (ic IngesterConfig) Source() string {
	if ic.Type == "adhoc" {
		return "adhoc"
	}
	return ic.Type + ":" + ic.Name
}

// DefaultSourceTimeout bounds one source's run (SPEC §17).
const DefaultSourceTimeout = 10 * time.Minute

// Assets is the [vaults.assets] table (SPEC §31.2/§31.3). Sizes are
// human-readable strings; the parsed byte values live in the *Bytes
// fields.
type Assets struct {
	Threshold   string `koanf:"threshold"`
	MaxDownload string `koanf:"max_download"`
	// TranscribeCmd/ProbeCmd are argv arrays (never shell strings, §31.7);
	// the local media path is appended as the final argument.
	TranscribeCmd []string `koanf:"transcribe_cmd"`
	ProbeCmd      []string `koanf:"probe_cmd"`
	HookTimeout   string   `koanf:"hook_timeout"`

	ThresholdBytes   int64         `koanf:"-"`
	MaxDownloadBytes int64         `koanf:"-"`
	HookTimeoutDur   time.Duration `koanf:"-"`
}

// DefaultAssetThreshold is 5 MB DECIMAL (SPEC §31.2): Obsidian Sync's
// Standard plan caps files at 5 MB, and a binary 5 MiB default would fail
// sync in exactly the 5.00–5.24 MB band the threshold exists to protect.
const DefaultAssetThreshold = 5_000_000

func (a *Assets) validate(vaultName string) error {
	a.ThresholdBytes = DefaultAssetThreshold
	a.MaxDownloadBytes = 100 << 20
	if a.Threshold != "" {
		n, err := ParseSize(a.Threshold)
		if err != nil {
			return fmt.Errorf("vault %q: [vaults.assets] threshold: %w", vaultName, err)
		}
		a.ThresholdBytes = n
	}
	if a.MaxDownload != "" {
		n, err := ParseSize(a.MaxDownload)
		if err != nil {
			return fmt.Errorf("vault %q: [vaults.assets] max_download: %w", vaultName, err)
		}
		a.MaxDownloadBytes = n
	}
	// The §31.3 table constraint: the download cap must exceed the
	// threshold, or every over-threshold remote asset is unreachable.
	if a.MaxDownloadBytes <= a.ThresholdBytes {
		return fmt.Errorf("vault %q: [vaults.assets] max_download (%d bytes) must exceed threshold (%d bytes); over-threshold downloads would be unreachable", vaultName, a.MaxDownloadBytes, a.ThresholdBytes)
	}
	// argv arrays only (koanf rejects a bare string with a type error at
	// load); guard the present-but-empty / empty-argv[0] shapes here.
	if err := validateHookArgv(vaultName, "transcribe_cmd", a.TranscribeCmd); err != nil {
		return err
	}
	if err := validateHookArgv(vaultName, "probe_cmd", a.ProbeCmd); err != nil {
		return err
	}
	a.HookTimeoutDur = 10 * time.Minute
	if a.HookTimeout != "" {
		d, err := time.ParseDuration(a.HookTimeout)
		if err != nil || d <= 0 {
			return fmt.Errorf("vault %q: [vaults.assets] hook_timeout %q: use a positive duration like \"10m\"", vaultName, a.HookTimeout)
		}
		a.HookTimeoutDur = d
	}
	return nil
}

// validateHookArgv rejects a present-but-unusable media hook (SPEC §31.7):
// an empty array, or one whose executable is blank.
func validateHookArgv(vaultName, key string, argv []string) error {
	if argv == nil {
		return nil
	}
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("vault %q: [vaults.assets] %s must be a non-empty argv array like [\"ffprobe\", \"-hide_banner\"], never a shell string", vaultName, key)
	}
	return nil
}

var sizeRe = regexp.MustCompile(`^(\d+)\s*(B|KB|MB|GB|KiB|MiB|GiB)?$`)

// ParseSize parses a human size string: decimal units (KB/MB/GB,
// 1000-based), binary units (KiB/MiB/GiB, 1024-based), or bare bytes.
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("bad size %q: use bytes or a unit suffix like \"5MB\" or \"25MiB\"", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q: %w", s, err)
	}
	mult := int64(1)
	switch m[2] {
	case "KB":
		mult = 1000
	case "MB":
		mult = 1000 * 1000
	case "GB":
		mult = 1000 * 1000 * 1000
	case "KiB":
		mult = 1 << 10
	case "MiB":
		mult = 1 << 20
	case "GiB":
		mult = 1 << 30
	}
	if n > (1<<62)/mult {
		return 0, fmt.Errorf("size %q overflows", s)
	}
	return n * mult, nil
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

		if err := v.Assets.validate(v.Name); err != nil {
			return err
		}
		if err := v.validateIngesters(); err != nil {
			return fmt.Errorf("vault %q: %w", v.Name, err)
		}
	}
	return nil
}

func (v *Vault) validateIngesters() error {
	names := map[string]bool{}
	for i, raw := range v.Ingesters {
		ic := IngesterConfig{Enabled: true, Timeout: DefaultSourceTimeout, Options: map[string]any{}}
		for k, val := range raw {
			switch k {
			case "type":
				ic.Type, _ = val.(string)
			case "name":
				ic.Name, _ = val.(string)
			case "enabled":
				b, ok := val.(bool)
				if !ok {
					return fmt.Errorf("ingesters[%d]: enabled must be a boolean", i)
				}
				ic.Enabled = b
			case "timeout":
				s, ok := val.(string)
				if !ok {
					return fmt.Errorf(`ingesters[%d]: timeout must be a duration string like "10m"`, i)
				}
				d, err := time.ParseDuration(s)
				if err != nil || d <= 0 {
					return fmt.Errorf("ingesters[%d]: bad timeout %q: use a positive duration like \"10m\"", i, s)
				}
				ic.Timeout = d
			default:
				ic.Options[k] = val
			}
		}
		if !vaultNameRe.MatchString(ic.Name) {
			return fmt.Errorf("ingesters[%d]: name %q must match %s", i, ic.Name, vaultNameRe)
		}
		if ic.Type == "" {
			return fmt.Errorf("ingester %q: type is required", ic.Name)
		}
		if names[ic.Name] {
			return fmt.Errorf("duplicate ingester name %q", ic.Name)
		}
		names[ic.Name] = true
		v.Sources = append(v.Sources, ic)
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
