package profile

import (
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"
)

// Companion to indexes_test.go. Those tests pin the §36 gate's accept/reject
// decision; these pin what the gate does NOT decide, and the coupling that
// makes the declarations worth validating at all.

// The declarations are inert today — the [lint.*] index tables are still the
// enforcement source (§36 defers that to §37). Two copies of the same
// contract drift silently, and the drift only becomes visible after the
// enforcement swap, as a behavior change nobody wrote. Pin the copies to
// each other now.
//
// The policy/severity column is the semantics each shipped rule hard-codes
// in internal/lint/rules/vaultwide.go (indexComplete{sev, reverse}); the
// profile package cannot import lint to read it, which is the same cycle
// that forced the severity literals to be duplicated.
func TestRdeggesIndexDeclarationsMatchTheLintTablesTheyMirror(t *testing.T) {
	want := map[string]struct{ policy, severity string }{
		"projects-linked-from-master":  {"must-link-all", "warning"},
		"resources-cataloged-in-index": {"must-link-all", "warning"},
		"recipes-index-links-complete": {"must-link-all-and-resolve", "error"},
	}

	p, err := Load("rdegges")
	require.NoError(t, err)

	byFile := map[string]Index{}
	for _, ix := range p.Indexes {
		byFile[ix.File] = ix
	}
	require.Len(t, byFile, len(want),
		"one [[indexes]] entry per shipped index rule — an extra or missing "+
			"declaration changes behavior the moment enforcement reads these")

	for id, sem := range want {
		cfg := p.Lint[id]
		require.NotEmpty(t, cfg, "the shipped %s rule table must exist", id)

		file, _ := cfg["file"].(string)
		lists, _ := cfg["lists"].(string)
		require.NotEmpty(t, file, "%s: table declares no file", id)
		require.NotEmpty(t, lists, "%s: table declares no lists glob", id)

		ix, ok := byFile[file]
		require.Truef(t, ok, "%s enforces %q but no [[indexes]] entry declares it", id, file)
		require.Equalf(t, lists, ix.Lists,
			"%s: the enforced glob and the declared glob disagree", id)
		require.Equalf(t, sem.policy, ix.Policy,
			"%s: the rule's reverse-resolution semantics and the declared policy disagree", id)
		require.Equalf(t, sem.severity, ix.Severity,
			"%s: the rule's severity and the declared severity disagree", id)

		// Where the table also states a severity override, all three must agree.
		if raw, present := cfg["severity"]; present {
			require.Equalf(t, sem.severity, raw, "%s: table severity override drifted", id)
		}
	}
}

// The other half of the locator promise: an entry with no `file` is named by
// its 1-based position, so a multi-entry profile still says WHICH entry.
// indexes_test.go pins the by-file half; without this one the positional
// branch could number from zero, or from the wrong loop variable, unnoticed.
func TestIndexEntryWithoutFileIsNamedByPosition(t *testing.T) {
	_, err := loadManifest(t, indexManifest([]Index{
		{File: "Projects.md", Lists: "Projects/**", Policy: "must-link-all", Severity: "warning"},
		{File: "index.md", Lists: "Resources/**", Policy: "must-link-all", Severity: "warning"},
		{Lists: "Recipes/*.md", Policy: "must-link-all", Severity: "error"},
	}))
	require.ErrorContains(t, err, "entry 3", "the third entry is entry 3, not entry 2")
	require.ErrorContains(t, err, "file is required")
}

// The gate treats `file` as an opaque string: no normalization, no
// vault-relative constraint. Pinned as OBSERVED behavior, not as a contract
// the change promised — see the tester report. It matters because the
// enforcement side looks the index up by exact key (ctx.Ix.Notes[file] over
// vault-relative note paths), so every spelling below designates no note.
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
