package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// §36 widened the frozen `indexes` view with `severity` and changed the human
// line to "(policy, severity)". profile_show_test.go pins one hard-coded line;
// these pin the whole surface, derived from the view so they survive a
// profile edit instead of becoming a second place to update.

// Every declared entry must render in both surfaces. Deriving the expected
// human line from the JSON view is the point: a new [[indexes]] entry that
// the text renderer forgets fails here without anyone editing this test.
func TestProfileShowRendersEveryDeclaredIndexInBothSurfaces(t *testing.T) {
	testEnv(t)

	v := decodeProfileView(t, "rdegges")
	require.NotEmpty(t, v.Indexes, "rdegges declares index rules")

	out, err := runCLI(t, "profile", "show", "rdegges")
	require.NoError(t, err, out)

	for _, ix := range v.Indexes {
		require.NotEmpty(t, ix.File)
		require.NotEmpty(t, ix.Lists)
		require.Contains(t, []string{"must-link-all", "must-link-all-and-resolve"}, ix.Policy,
			"%s: the view carries a known policy", ix.File)
		require.Contains(t, []string{"error", "warning"}, ix.Severity,
			"%s: §36 requires a known severity on every entry", ix.File)

		want := fmt.Sprintf("  %s lists %s (%s, %s)\n", ix.File, ix.Lists, ix.Policy, ix.Severity)
		require.Contains(t, out, want, "the text rendering must carry every declared entry")
	}

	// The strictest entry is the one the old single-line assertion never
	// reached: a non-default policy AND a non-default severity.
	require.Contains(t, out,
		"Resources/Personal/Recipes/Recipes.md lists Resources/Personal/Recipes/*.md "+
			"(must-link-all-and-resolve, error)")
}

// The severity key is unconditional in the frozen JSON shape — no omitempty,
// so a consumer can read it without a presence check. Asserted on the raw
// bytes because a decoded struct cannot tell an absent key from an empty one.
func TestProfileShowJSONAlwaysCarriesIndexSeverityKey(t *testing.T) {
	testEnv(t)

	raw, err := runCLI(t, "profile", "show", "--json", "rdegges")
	require.NoError(t, err, raw)

	var probe struct {
		Indexes []map[string]json.RawMessage `json:"indexes"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &probe))
	require.NotEmpty(t, probe.Indexes)
	for i, entry := range probe.Indexes {
		require.Contains(t, entry, "severity", "entry %d omits the severity key", i)
		require.ElementsMatch(t, []string{"file", "lists", "policy", "severity"},
			keysOf(entry), "entry %d: the frozen index shape is exactly these four keys", i)
	}
}

// para declares no entries: the JSON list is empty-but-present (nonNil), and
// the human rendering skips the section entirely rather than printing a
// header with nothing under it.
func TestProfileShowIndexSectionAbsentWhenNoneDeclared(t *testing.T) {
	testEnv(t)

	raw, err := runCLI(t, "profile", "show", "--json", "para")
	require.NoError(t, err, raw)
	require.Contains(t, strings.ReplaceAll(raw, " ", ""), `"indexes":[]`,
		"an index-less profile emits an empty list, never null")

	out, err := runCLI(t, "profile", "show", "para")
	require.NoError(t, err, out)
	require.NotContains(t, out, "\nindexes:\n", "no entries, no section")
}

// The upgrade path §36 creates: a profile ejected from a pre-§36 binary has
// [[indexes]] entries with no severity, and severity is required rather than
// defaulted. Every command that loads that profile must fail closed, loudly,
// and name the entry — never fall back to a silent severity.
func TestEjectedPreSeverityProfileFailsClosedWithAnActionableError(t *testing.T) {
	testEnv(t)

	dest := filepath.Join(t.TempDir(), "legacy")
	out, err := runCLI(t, "profile", "eject", "rdegges", dest)
	require.NoError(t, err, out)

	manifestPath := filepath.Join(dest, "profile.toml")
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	// Roll the manifest back to its pre-§36 shape: drop the severity line
	// that follows each [[indexes]] policy (leaving the [lint.*] severity
	// overrides alone) and restore the old recipes policy.
	aged := regexp.MustCompile(`(?m)^(policy = "[^"]*"\n)severity = "(error|warning)"\n`).
		ReplaceAllString(string(raw), "$1")
	aged = strings.ReplaceAll(aged, `policy = "must-link-all-and-resolve"`, `policy = "must-link-all"`)
	require.NotEqual(t, string(raw), aged, "fixture ageing must apply")
	require.NotContains(t, aged, "must-link-all-and-resolve")
	require.NoError(t, os.WriteFile(manifestPath, []byte(aged), 0o644))

	_, err = runCLI(t, "profile", "show", dest)
	require.Error(t, err, "a pre-§36 profile must not load silently")
	require.ErrorContains(t, err, "severity")
	require.ErrorContains(t, err, "indexes", "the error names the offending table")
	require.ErrorContains(t, err, "Projects.md", "and the offending entry")
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
