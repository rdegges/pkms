package profile

import (
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"
)

// The profile-side twin of
// rules.TestAcceptedGlobMustNotSilentlyMissTheLiteralBranchItNames, and the
// higher-severity surface of the two: a scope that classifies nothing does
// not just disable one rule, it makes every note of that type unclassified —
// which drives placement, schema selection and severity.
//
// doublestar v4.10.0's ValidatePattern honours a character class when it
// parses `{...}`; its matcher splits the alternation on every comma and does
// not. So `{[a,b]x.md,People/Snyk/Jane Doe.md}` validates, and then
// MatchUnvalidated answers "no" for every path, including the one the second
// alternation branch names as a plain literal.
//
// GAP (pkms issue #38): this pins CURRENT behavior, not desired behavior —
// the scope loads and then silently classifies nothing. Ruled a documented
// KnownGap: it predates this branch (base main swallowed Match's error to
// the same effect), and no shipped profile glob is affected. If a later
// change closes it, this test fails — that failure is the fix landing, and
// the test should be inverted, not deleted.
func TestKnownGap_CommaClassInAlternationClassifiesNothing(t *testing.T) {
	const rel = "People/Snyk/Jane Doe.md"
	const glob = "{[a,b]x.md," + rel + "}"

	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the load-time check accepts this scope")
	_, matchErr := doublestar.Match(glob, rel)
	require.Error(t, matchErr,
		"premise: doublestar's matcher cannot parse this pattern at all")

	p, err := loadManifest(t, Manifest{
		SchemaVersion: SupportedSchemaVersion, Name: "designation",
		Types: []Type{{Name: "note", Scope: []string{glob}}},
	})
	require.NoError(t, err, "GAP: the divergent scope loads, not rejected")
	require.Equalf(t, "", p.TypeOf(rel, nil),
		"GAP: MatchUnvalidated cannot parse the pattern and silently reports no "+
			"match, so the note the literal branch names goes unclassified (issue #38, %q)", rel)
}

// The construction gate could catch part of this class without any
// vault-dependent behaviour: doublestar.Match(g, "") is a single,
// deterministic call. This records exactly how much it would buy — it catches
// the pattern above, and misses the same defect when the bad construct sits
// after the first path segment.
//
// PROPOSED CONTRACT, not a defect proof: it asserts today's doublestar
// behaviour so the next round can act on measured numbers rather than a
// claim. It passes.
func TestProposedContract_EmptyNameProbeCatchesTheLeadingAlternationOnly(t *testing.T) {
	catchable := "{[a,b]x.md,People/Snyk/Jane Doe.md}"
	_, err := doublestar.Match(catchable, "")
	require.Error(t, err,
		"a construction-time Match(g, \"\") probe would reject %q", catchable)

	missed := "People/{[a,b]x,Snyk}/Jane Doe.md"
	require.True(t, doublestar.ValidatePattern(missed))
	_, err = doublestar.Match(missed, "")
	require.NoError(t, err,
		"the probe cannot see past the first path segment, so %q escapes it", missed)
	_, err = doublestar.Match(missed, "People/Snyk/Jane Doe.md")
	require.Error(t, err, "yet the matcher still refuses %q on a real path", missed)
	require.False(t, doublestar.MatchUnvalidated(missed, "People/Snyk/Jane Doe.md"),
		"and MatchUnvalidated silently misses the path it names")

	// The probe must not reject any glob the shipped profiles use.
	for _, name := range Builtins() {
		p, err := Load(name)
		require.NoError(t, err)
		for _, ty := range p.Types {
			for _, g := range ty.Scope {
				_, err := doublestar.Match(g, "")
				require.NoErrorf(t, err,
					"profile %q type %q glob %q would be a false alarm for the probe",
					name, ty.Name, g)
			}
		}
	}
}
