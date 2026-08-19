package profile

import (
	"testing"
	"testing/fstest"

	"github.com/bmatcuk/doublestar/v4"
	toml "github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

// The #30 fix keeps the load-time glob check syntax-only (ValidatePattern)
// and makes TypeOf surface doublestar's match-time error to its caller.
// These tests hold that pair to the real invariant: a match-time error is
// always surfaced — never converted to "unclassified" — and every glob shape
// the shipped profiles use still loads and still classifies.

// loadManifest builds a one-type profile in memory. Going through the TOML
// encoder keeps arbitrary fuzz bytes from becoming a decode error that would
// mask the check under test.
func loadManifest(tb testing.TB, m Manifest) (*Profile, error) {
	tb.Helper()
	raw, err := toml.Marshal(m)
	require.NoError(tb, err)
	return load(fstest.MapFS{"profile.toml": &fstest.MapFile{Data: raw}}, "")
}

// classifyNames is the corpus of vault-relative paths TypeOf runs a scope
// glob against.
var classifyNames = []string{
	"", "Now.md", "a", "a/b", "a/b/c", "Areas/Personal/x.md",
	"Meetings/Snyk/2026/05/06/1100 - Sync.md", "Café/naïve.md",
	"{braces}.md", "[brackets].md", "back\\slash.md", "A",
}

// The invariant behind the design: whenever doublestar.Match errors on the
// path TypeOf is classifying, TypeOf must return that error — and it must
// error ONLY then. Load proves syntax only (doublestar re-validates the
// pattern SUFFIX where matching stops, so some syntactically valid patterns
// still error), which makes the classification path the load-bearing one.
func FuzzScopeGlobMatchErrorSurfacesFromTypeOf(f *testing.F) {
	for _, seed := range []string{
		"Areas/**", "**/*.md", "Resources/{Snyk,Personal}/*.md",
		"Meetings/*/[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*.md",
		"[unclosed", "{a,b", "}", "[]", `\`, "**", "*", "?",
		// Known ValidatePattern/Match divergences: a suffix that splits a
		// character class holding '{' or '}' does not re-validate.
		"{[}]}", "[!a{b]", "[!00A{000]",
		"People/Snyk/Jane Doe.md{[,],}",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, glob string) {
		p, err := loadManifest(t, Manifest{
			SchemaVersion: SupportedSchemaVersion,
			Name:          "fuzz",
			Types:         []Type{{Name: "note", Scope: []string{glob}}},
		})
		if err != nil {
			return // rejected at load: fail-closed, nothing left to prove
		}
		require.NotNil(t, p)
		for _, n := range classifyNames {
			_, matchErr := doublestar.Match(glob, n)
			typ, typErr := p.TypeOf(n, nil)
			if matchErr != nil && typErr == nil {
				t.Fatalf("scope glob %q errors matching %q (%v) but TypeOf returned "+
					"(%q, nil) — the match error was converted to unclassified",
					glob, n, matchErr, typ)
			}
			if matchErr == nil && typErr != nil {
				t.Fatalf("scope glob %q matches %q cleanly but TypeOf errored: %v",
					glob, n, typErr)
			}
		}
	})
}

// The concrete, minimal statement of the invariant: a glob doublestar
// validates as a whole but refuses to match passes load, so the error must
// surface from TypeOf — never read as "unclassified", which would misfile
// every note of the type in silence.
func TestGlobMatchErrorSurfacesFromTypeOf(t *testing.T) {
	// "{[}]}" is a character class holding '}' inside an alternation.
	// ValidatePattern accepts it; Match rejects it for every path.
	const glob = "{[}]}"
	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the load-time check accepts this pattern")
	_, matchErr := doublestar.Match(glob, "x")
	require.Error(t, matchErr,
		"premise: doublestar refuses the same pattern while matching")

	p, err := loadManifest(t, Manifest{
		SchemaVersion: SupportedSchemaVersion,
		Name:          "divergent",
		Types:         []Type{{Name: "note", Scope: []string{glob}}},
	})
	require.NoError(t, err, "syntax-valid globs load; match-safety is enforced per call")

	typ, typErr := p.TypeOf("x", nil)
	require.Error(t, typErr,
		"the match error must surface from TypeOf, never read as unclassified")
	require.Empty(t, typ)
	require.ErrorContains(t, typErr, glob, "the error must name the offending pattern")
	require.ErrorContains(t, typErr, "note", "the error must name the type")
}

// DEFECT PROOF (#30 is not closed on the profile side either). load() gates
// on ValidatePattern plus the same five-path probe corpus, and doublestar
// re-validates the pattern SUFFIX wherever matching stops — a position those
// five probes rarely reach. FuzzLoadedScopeGlobIsMatchable above finds a
// counterexample in under a second of real fuzzing (`go test -fuzz` on this
// package); its minimised input is pinned in testdata/fuzz.
//
// The consequence is the one the fix was for: the profile loads clean and
// then classifies nothing. The assertion is fix-agnostic — rejecting at load
// and surfacing the match error are both valid repairs.
func TestLoadedScopeGlobMustNotSilentlyMissThePathItNames(t *testing.T) {
	const rel = "People/Snyk/Jane Doe.md"
	// The literal path plus "{[,],}" — "one comma" or "nothing" — so the
	// scope still designates exactly that note.
	const glob = rel + "{[,],}"

	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the load-time check accepts this pattern")
	require.True(t, doublestar.MatchUnvalidated(glob, rel),
		"premise: the scope does designate %q", rel)
	_, matchErr := doublestar.Match(glob, rel)
	require.Errorf(t, matchErr, "premise: Match refuses %q while matching %q", glob, rel)

	p, err := loadManifest(t, Manifest{
		SchemaVersion: SupportedSchemaVersion, Name: "divergent",
		Types: []Type{{Name: "note", Scope: []string{glob}}},
	})
	if err != nil {
		return // rejected at load: fail-closed, nothing left to prove
	}
	typ, typErr := p.TypeOf(rel, nil)
	if typErr != nil {
		return // the match error surfaced: fail-closed, nothing left to prove
	}
	require.Equalf(t, "note", typ,
		"the profile loaded and TypeOf did not error, so this scope must classify "+
			"the note it names — anything else is a silently dropped match error (%q)", rel)
}

// A malformed glob must be caught wherever it sits: any type, any position in
// that type's scope list.
func TestLoadRejectsAMalformedGlobInEveryPosition(t *testing.T) {
	for label, types := range map[string][]Type{
		"only type, only glob":  {{Name: "a", Scope: []string{"[unclosed"}}},
		"only type, first glob": {{Name: "a", Scope: []string{"[unclosed", "Areas/**"}}},
		"only type, last glob":  {{Name: "a", Scope: []string{"Areas/**", "[unclosed"}}},
		"only type, mid glob":   {{Name: "a", Scope: []string{"Areas/**", "[unclosed", "People/**"}}},
		"first of many types": {
			{Name: "a", Scope: []string{"[unclosed"}},
			{Name: "b", Scope: []string{"Areas/**"}},
		},
		"last of many types": {
			{Name: "a", Scope: []string{"Areas/**"}},
			{Name: "b", Scope: []string{"[unclosed"}},
		},
	} {
		t.Run(label, func(t *testing.T) {
			_, err := loadManifest(t, Manifest{
				SchemaVersion: SupportedSchemaVersion, Name: "bad", Types: types,
			})
			require.Error(t, err, "a malformed scope glob must fail load")
			require.ErrorContains(t, err, "[unclosed", "the error must name the pattern")
		})
	}
}

// Every malformed doublestar shape, not just the unterminated class the
// change's own test uses.
func TestLoadRejectsEveryMalformedGlobShape(t *testing.T) {
	for _, g := range []string{
		"[unclosed", "[a-", "[^", "[!", "[]", "a[]b", "[]]",
		"Areas/[", `Areas\`, `[\]`, "{a,b", "}", "Areas/{a",
	} {
		t.Run(g, func(t *testing.T) {
			require.Falsef(t, doublestar.ValidatePattern(g),
				"premise: doublestar must reject %q", g)
			_, err := loadManifest(t, Manifest{
				SchemaVersion: SupportedSchemaVersion, Name: "bad",
				Types: []Type{{Name: "note", Scope: []string{g}}},
			})
			require.Errorf(t, err, "malformed scope glob %q must fail load", g)
			require.ErrorContains(t, err, "note", "the error must name the type")
		})
	}
}

// A fail-closed check that rejects good config is worse than the bug it
// fixes: every glob shape a profile may reasonably use must still load.
func TestLoadAcceptsTheGlobShapesProfilesUse(t *testing.T) {
	for _, g := range []string{
		"Areas/**", "Areas/**/*.md", "Archive/**", "Now.md",
		"People/Snyk/*.md", "Resources/{Snyk,Personal}/*.md",
		"Meetings/*/[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*.md",
		"Resources/Personal/Session Traces/*.md", // spaces
		"[!x]*.md", `\[lit\].md`, "Café/**", "Записи/**", "*", "**", "?.md",
	} {
		t.Run(g, func(t *testing.T) {
			_, err := loadManifest(t, Manifest{
				SchemaVersion: SupportedSchemaVersion, Name: "ok",
				Types: []Type{{Name: "note", Scope: []string{g}}},
			})
			require.NoErrorf(t, err, "valid scope glob %q must load", g)
		})
	}
}

// A type with no scope at all is not a malformed glob — it must keep loading
// (the empty-list branch of the new loop).
func TestLoadAcceptsATypeWithNoScope(t *testing.T) {
	p, err := loadManifest(t, Manifest{
		SchemaVersion: SupportedSchemaVersion, Name: "ok",
		Types: []Type{{Name: "note"}, {Name: "other", Scope: []string{}}},
	})
	require.NoError(t, err)
	typ, typErr := p.TypeOf("anything.md", nil)
	require.NoError(t, typErr)
	require.Equal(t, "", typ, "a scopeless type matches nothing")
}

// The shipped profiles must survive their own new gate, and classification
// must be unchanged by it: the load-time check is validation, not rewriting.
func TestBuiltinScopeGlobsValidateAndStillClassify(t *testing.T) {
	for _, name := range Builtins() {
		t.Run(name, func(t *testing.T) {
			p, err := Load(name)
			require.NoError(t, err)
			require.NotEmpty(t, p.Types)
			for _, ty := range p.Types {
				for _, g := range ty.Scope {
					require.Truef(t, doublestar.ValidatePattern(g),
						"profile %q type %q ships an unvalidatable scope glob %q", name, ty.Name, g)
					// And the stronger property the code relies on.
					for _, n := range classifyNames {
						_, err := doublestar.Match(g, n)
						require.NoErrorf(t, err,
							"profile %q type %q glob %q errors matching %q", name, ty.Name, g, n)
					}
				}
			}
		})
	}

	// Regression: the gate did not change what the rdegges profile classifies.
	p, err := Load("rdegges")
	require.NoError(t, err)
	for path, want := range map[string]string{
		"People/Snyk/Jane Doe.md":                         "person",
		"Meetings/Snyk/2026/05/06/1100 - Sync.md":         "meeting",
		"Meetings/Snyk/2026/05/06/daily-brief.md":         "daily-brief",
		"Projects/Snyk/Thing.md":                          "project",
		"Areas/Personal/Health.md":                        "area",
		"Archive/Personal/Old.md":                         "archived",
		"Resources/Personal/Recipes/Soup.md":              "recipe",
		"Resources/Personal/Session Traces/2026-05-06.md": "session-trace",
		"nowhere/at/all.md":                               "",
	} {
		got, typErr := p.TypeOf(path, nil)
		require.NoErrorf(t, typErr, "TypeOf(%q)", path)
		require.Equalf(t, want, got, "TypeOf(%q)", path)
	}
}
