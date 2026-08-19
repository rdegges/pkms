package rules_test

import (
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
)

// #30's residue, measured in round 4.
//
// doublestar v4.10.0's ValidatePattern and its matcher implement DIFFERENT
// grammars for a character class nested inside an alternation. ValidatePattern
// parses the whole pattern eagerly and honours `[...]`, so it accepts
// `{[a,b]x.md,real/path.txt}`. The matcher splits `{...}` on every comma
// without honouring the class, so it derives the malformed branch `[a` —
// doublestar.Match reports "syntax error in pattern", and
// doublestar.MatchUnvalidated (which is Match with validation switched off)
// silently answers "no match" for EVERY name.
//
// The consequence for pkms is the original #30 symptom, reached by a narrower
// path: a scope glob the construction gate accepted selects nothing at all,
// so the rule is silently disabled. Here the SECOND alternation branch is the
// plain literal path of a real file, so the pattern unambiguously designates
// it — no grammar interpretation is in dispute.
//
// Measured over every ValidatePattern-accepted pattern of length <= 5 over
// the alphabet `a/[]{}!-\*,A` (80,199 patterns): 3 are refused by the matcher
// for every name, and a construction-time `doublestar.Match(g, "")` probe
// catches all 3 with zero false alarms. That probe does not catch the case
// where the bad construct sits after the first path segment
// (`Areas/{[a,b]x,Personal}/junk.txt`), because the matcher stops walking the
// pattern before it reaches the alternation.
//
// GAP (issue #38): this pins CURRENT behavior, not desired behavior — the
// accepted glob silently matches nothing, so the rule is disabled without
// a word. The BDFL ruled the residual a documented KnownGap: it predates
// this branch (base main swallowed Match's error to the same effect), no
// shipped profile glob is affected, and the Match(g, "") probe was
// rejected as a half-measure. If a later change closes it (an upstream
// doublestar fix or a ruled local gate), this test fails — that failure is
// the fix landing, and the test should be inverted, not deleted.
func TestKnownGap_CommaClassInAlternationSilentlyMatchesNothing(t *testing.T) {
	const junk = "Areas/Personal/junk.txt"
	// Branch 1 is a character class holding a comma; branch 2 is the literal
	// path. Whatever branch 1 means, branch 2 designates exactly `junk`.
	const glob = "{[a,b]x.md," + junk + "}"

	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the construction gate's check accepts this pattern")
	_, matchErr := doublestar.Match(glob, junk)
	require.Error(t, matchErr,
		"premise: doublestar's matcher cannot parse this pattern at all")

	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md": "x\n",
		junk:                     "x\n",
	})

	// The fixture really does hold a violation: a literal scope finds it.
	fs, err := lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{junk}}},
		[]string{"non-markdown-in-note-folders"})
	require.NoError(t, err)
	require.Lenf(t, fs, 1, "premise: %q is a finding under a literal scope: %+v", junk, fs)

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{glob}}},
		[]string{"non-markdown-in-note-folders"})
	require.NoError(t, err, "GAP: the divergent glob is accepted, not rejected")
	require.Emptyf(t, fs,
		"GAP: MatchUnvalidated cannot parse the pattern and silently reports "+
			"no match, so the literal branch never selects %q (issue #38): %+v", junk, fs)
}

// GAP (issue #38): the same silent no-match on the glob-configured scalar
// keys, pinned so the gap is not read as one rule's quirk. `lists`/`counts`
// drive index-completeness and count-drift; a scope that selects nothing
// turns those rules off. Invert when #38 closes, do not delete.
func TestKnownGap_CommaClassInAlternationEmptiesScalarGlobKeys(t *testing.T) {
	const recipe = "Resources/Personal/Recipes/Soup.md"
	const glob = "{[a,b]x.md," + recipe + "}"
	require.True(t, doublestar.ValidatePattern(glob), "premise: construction accepts it")

	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		// An index that lists nothing, so every recipe the glob selects is a
		// finding. With a working glob the count is 1; with a silent
		// no-match it is 0.
		"Resources/Personal/Recipes/Recipes.md": "---\nrecipe_count: 0\n---\n# Recipes\n",
		recipe:                                  "---\ntitle: Soup\n---\nbody\n",
	})

	base := map[string]any{"file": "Resources/Personal/Recipes/Recipes.md"}
	literal, err := lint.Run(ix, prof, map[string]map[string]any{
		"recipes-index-links-complete": {"file": base["file"], "lists": recipe},
	}, []string{"recipes-index-links-complete"})
	require.NoError(t, err)
	require.NotEmptyf(t, literal,
		"premise: a literal glob makes the uncatalogued recipe a finding: %+v", literal)

	got, err := lint.Run(ix, prof, map[string]map[string]any{
		"recipes-index-links-complete": {"file": base["file"], "lists": glob},
	}, []string{"recipes-index-links-complete"})
	require.NoError(t, err, "GAP: the divergent glob is accepted, not rejected")
	require.Emptyf(t, got,
		"GAP: the glob names %q in a literal alternation branch but selects "+
			"nothing, so the rule reports clean (issue #38): %+v", recipe, got)
}
