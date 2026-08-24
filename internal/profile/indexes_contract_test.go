package profile

import (
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"
)

// Companion to indexes_test.go. Those tests pin the §36 gate's accept/reject
// decision; these pin what the gate does NOT decide. (The mirror test that
// held [[indexes]] and the retired [lint.*] index tables together retired
// with those tables at §37 — the declarations are now the only copy.)

// `file` must be a clean vault-relative path: not absolute, no ".." element,
// and equal to its own path.Clean. Anything else could never designate a
// note once §37 looks entries up by exact vault-relative key — a contract
// that can never be checked must not load (§36 gate condition).
func TestIndexFileMustBeACleanVaultRelativePath(t *testing.T) {
	for label, file := range map[string]string{
		"parent traversal":   "../../etc/passwd",
		"dotdot mid-path":    "Projects/../Projects.md",
		"bare dotdot prefix": "../Projects.md",
		"absolute path":      "/etc/passwd",
		"leading dot slash":  "./Projects.md",
		"trailing slash":     "Projects.md/",
	} {
		t.Run(label, func(t *testing.T) {
			_, err := loadManifest(t, indexManifest([]Index{{
				File: file, Lists: "Projects/**/*.md",
				Policy: "must-link-all", Severity: "warning",
			}}))
			require.Error(t, err, "an unnormalized file must be rejected at load")
			require.ErrorContains(t, err, "vault-relative")
		})
	}
}

// The rule's deliberate bounds, pinned: shapes that ARE clean relative
// paths load even when they look odd — a whitespace or backslash basename
// is legal on-disk content, and rejecting it would guess at filesystems.
func TestOddButCleanIndexFilesStillLoad(t *testing.T) {
	for label, file := range map[string]string{
		"whitespace only": "   ",
		"backslashes":     `Projects\Projects.md`,
	} {
		t.Run(label, func(t *testing.T) {
			p, err := loadManifest(t, indexManifest([]Index{{
				File: file, Lists: "Projects/**/*.md",
				Policy: "must-link-all", Severity: "warning",
			}}))
			require.NoError(t, err)
			require.Equal(t, file, p.Indexes[0].File)
		})
	}
}

// Uniqueness is exact-string over CLEAN paths. The path-shape gate removes
// the alternate spellings of one file (./x, x/), so the remaining
// non-colliding pairs really are distinct clean paths — case and trailing
// space differences are different files as far as the vault key goes.
func TestDuplicateIndexFilesAreDetectedByExactStringOnly(t *testing.T) {
	for label, second := range map[string]string{
		"case difference": "projects.md",
		"trailing space":  "Projects.md ",
	} {
		t.Run(label, func(t *testing.T) {
			p, err := loadManifest(t, indexManifest([]Index{
				{File: "Projects.md", Lists: "Projects/**", Policy: "must-link-all", Severity: "warning"},
				{File: second, Lists: "Projects/**", Policy: "must-link-all", Severity: "warning"},
			}))
			require.NoError(t, err, "observed: only byte-identical `file` values collide")
			require.Len(t, p.Indexes, 2)
		})
	}

	// The exact-string case does fail, and that is the promised behavior.
	_, err := loadManifest(t, indexManifest([]Index{
		{File: "Projects.md", Lists: "Projects/**", Policy: "must-link-all", Severity: "warning"},
		{File: "Projects.md", Lists: "Other/**", Policy: "must-link-all", Severity: "error"},
	}))
	require.ErrorContains(t, err, "duplicate")
}

// `lists` reuses the §30 construction gate (ValidatePattern), which accepts
// patterns doublestar.Match then refuses at match time. That divergence is
// exactly the #30 defect, one field over. Pinned so the requirement it
// places on the enforcement side stays written down: whatever consumes
// `lists` must match with MatchUnvalidated (rules.matchAnyGlob does today),
// never with Match-and-drop-the-error.
func TestListsGlobInheritsTheScopeGlobDivergence(t *testing.T) {
	const glob = "{[}]}" // a character class holding '}' inside an alternation
	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the load-time check accepts this pattern")
	_, matchErr := doublestar.Match(glob, "x.md")
	require.Error(t, matchErr,
		"premise: doublestar refuses the same pattern while matching")

	p, err := loadManifest(t, indexManifest([]Index{{
		File: "index.md", Lists: glob, Policy: "must-link-all", Severity: "warning",
	}}))
	require.NoError(t, err, "syntax-valid lists globs load, same as scopes")
	require.Equal(t, glob, p.Indexes[0].Lists)
	require.False(t, doublestar.MatchUnvalidated(glob, "x.md"),
		"and MatchUnvalidated answers deterministically instead of erroring")
}

// The gate is total: for any field values, load either rejects the entry or
// returns one that satisfies every property §36 promises. No input reaches a
// loaded Profile with an empty file, an unevaluable glob, or an off-enum
// policy/severity.
func FuzzIndexEntryValidationIsTotal(f *testing.F) {
	for _, seed := range [][4]string{
		{"index.md", "Resources/**/*.md", "must-link-all", "warning"},
		{"Recipes.md", "Recipes/*.md", "must-link-all-and-resolve", "error"},
		{"", "", "", ""},
		{"index.md", "[unclosed", "must-link-all", "warning"},
		{"index.md", "{[}]}", "must-link-all", "error"},
		{"index.md", "**", "MUST-LINK-ALL", "Warning"},
		{"index.md", "**", " must-link-all ", " error "},
		{"index.md", "**", "must-link-all", "info"},
		// Everything else valid, one enum field empty: the seeds that catch
		// a "required" field quietly becoming optional.
		{"index.md", "**", "must-link-all", ""},
		{"index.md", "**", "", "error"},
		{"../x.md", "**", "must-link-all", "error"},
		{"Café/naïve.md", "Café/**", "must-link-all", "warning"},
		{"a\x00b.md", "**", "must-link-all", "error"},
	} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}

	f.Fuzz(func(t *testing.T, file, lists, policy, severity string) {
		p, err := loadManifest(t, indexManifest([]Index{{
			File: file, Lists: lists, Policy: policy, Severity: severity,
		}}))
		if err != nil {
			return // rejected at load: fail-closed, nothing left to prove
		}
		require.Len(t, p.Indexes, 1)
		got := p.Indexes[0]
		require.NotEmpty(t, got.File, "a loaded entry always names a file")
		require.NotEmpty(t, got.Lists, "a loaded entry always carries a glob")
		require.True(t, doublestar.ValidatePattern(got.Lists),
			"a loaded glob always evaluates: %q", got.Lists)
		require.Contains(t, []string{"must-link-all", "must-link-all-and-resolve"},
			got.Policy, "a loaded entry always carries a known policy")
		require.Contains(t, []string{"error", "warning"},
			got.Severity, "a loaded entry always carries a known severity")
	})
}
