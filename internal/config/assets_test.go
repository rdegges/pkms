package config

import (
	"testing"

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
