package rules_test

import (
	"strconv"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
)

// The #30 fix keeps validGlobs as a syntax-only gate (ValidatePattern) and
// matches with doublestar.MatchUnvalidated — the library's documented
// pairing for pre-validated patterns. These tests hold that pair to the
// final invariant: an accepted glob always evaluates (never errors, never
// converts to no-match), MatchUnvalidated agrees with Match wherever Match
// succeeds, and every glob shape real config uses keeps working.

// The invariant behind the design: a pattern either fails construction
// (ValidatePattern rejects it) or evaluates cleanly — lint.Run never errors
// on an accepted glob, and where doublestar.Match succeeds, the
// MatchUnvalidated verdict the engine uses agrees with it.
func FuzzAcceptedGlobAlwaysEvaluates(f *testing.F) {
	for _, seed := range []string{
		"Areas/**", "**/*.md", "Resources/{Snyk,Personal}/*.md",
		"Meetings/*/[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*.md",
		"[unclosed", "[a-", "[]", "[^", "[!", "{a,b", "}", "{a,{b,c}}",
		"{}", "{,}", "[z-a]", "[--]", "[a-]", "[[]", "[]]", `a\`, `\`,
		"**", "a**b", "*", "?", "[^]a]", "{a/b,c}", "café*", "\x00",
		"{{}}", "{a,b}{c,d}", "[\\]", "**/**/**",
		// Known ValidatePattern/Match divergences: a suffix that splits a
		// character class holding '{' or '}' does not re-validate.
		"{[}]}", "[!a{b]", "[!00A{000]",
		"Areas/Personal/junk.txt{[,],}",
	} {
		f.Add(seed)
	}
	// The rule skips .md files before matching, so the only path it feeds
	// to doublestar.Match is the .txt file.
	const junk = "Areas/Personal/junk.txt"
	ix, prof := buildVaultWith(f, "rdegges", map[string]string{
		"Areas/Personal/n.md": "x\n",
		junk:                  "x\n",
	})

	f.Fuzz(func(t *testing.T, pattern string) {
		over := map[string]map[string]any{
			"non-markdown-in-note-folders": {"scopes": []any{pattern}},
		}
		fs, runErr := lint.Run(ix, prof, over, []string{"non-markdown-in-note-folders"})
		if !doublestar.ValidatePattern(pattern) {
			if runErr == nil {
				t.Fatalf("glob %q fails ValidatePattern but the run accepted it", pattern)
			}
			return
		}
		if runErr != nil {
			t.Fatalf("glob %q passes ValidatePattern but the run errored: %v — "+
				"an accepted glob must always evaluate", pattern, runErr)
		}
		// The engine's verdict is MatchUnvalidated's; where Match succeeds
		// the two must agree.
		want := doublestar.MatchUnvalidated(pattern, junk)
		if got := len(fs) == 1; got != want {
			t.Fatalf("glob %q: run found=%v but MatchUnvalidated says %v", pattern, got, want)
		}
		if ok, err := doublestar.Match(pattern, junk); err == nil && ok != want {
			t.Fatalf("glob %q: MatchUnvalidated=%v disagrees with successful Match=%v",
				pattern, want, ok)
		}
	})
}

// The original #30 defect proof, kept as a regression pin. doublestar.Match
// re-validates the pattern SUFFIX at whatever position matching stops, so
// globs ValidatePattern accepts can still error in Match — and before the
// fix, matchAnyGlob dropped that error and the scope silently matched
// nothing. (A brute-force sweep of all patterns up to length 6 over the
// alphabet `a/[]{}!-\*,A` found 613 such globs.)
//
// The assertion is deliberately fix-agnostic: rejecting the glob,
// surfacing the error, and evaluating with MatchUnvalidated are all valid
// repairs. The shipped code takes the third route — matchAnyGlob matches
// with MatchUnvalidated — so the run accepts the glob and the main
// assertion fires: the scope selects the file it designates.
func TestAcceptedGlobMustNotSilentlyExcludeTheFileItNames(t *testing.T) {
	const junk = "Areas/Personal/junk.txt"
	// The literal path plus "{[,],}" — an alternation of "one comma" or
	// "nothing", so the pattern still designates exactly that one file.
	const glob = junk + "{[,],}"

	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the gate's first check accepts this pattern")
	require.True(t, doublestar.MatchUnvalidated(glob, junk),
		"premise: the pattern does designate %q", junk)
	_, matchErr := doublestar.Match(glob, junk)
	require.Errorf(t, matchErr,
		"premise: Match refuses %q while matching %q", glob, junk)

	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md": "x\n",
		junk:                     "x\n",
	})
	// A plain literal scope finds the file, so the fixture is not simply
	// free of violations.
	fs, err := lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{junk}}},
		[]string{"non-markdown-in-note-folders"})
	require.NoError(t, err)
	require.Lenf(t, fs, 1, "premise: %q is a finding under a literal scope: %+v", junk, fs)

	fs, err = lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{glob}}},
		[]string{"non-markdown-in-note-folders"})
	if err != nil {
		return // rejected (construction or match time): fail-closed, nothing left to prove
	}
	require.Lenf(t, fs, 1,
		"the run accepted the glob, so the scope must still select the file it names; "+
			"matchAnyGlob drops doublestar's error instead and the rule reports clean: %+v", fs)
}

// A fail-closed check that rejects good config is worse than the bug it
// fixes. Every glob shape the shipped profiles use — and the wider
// doublestar syntax a user may reasonably reach for — must survive, on every
// glob-configured key.
func TestValidGlobsAcceptedOnEveryGlobConfiguredKey(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	valid := []string{
		"Areas/**",                           // recursive
		"Areas/**/*.md",                      // recursive + extension
		"Resources/{Snyk,Personal}/*.md",     // alternation (shipped)
		"Projects/{Snyk,Personal}/*.md",      // alternation (shipped)
		"Resources/Personal/Recipes/*.md",    // shipped
		"Meetings/*/[0-9][0-9][0-9][0-9]/**", // character classes (shipped shape)
		"People/**", "*", "**", "?.md",
		"[!x]*.md",   // negated class
		`\[lit\].md`, // escaped brackets
		"Café/**",    // non-ASCII
		"Записи/**",  // non-ASCII, non-Latin
	}
	listKeys := map[string]string{"orphan-notes": "scopes", "non-markdown-in-note-folders": "scopes"}
	scalarKeys := map[string][2]string{
		"resources-cataloged-in-index": {"index.md", "lists"},
		"projects-linked-from-master":  {"Projects.md", "lists"},
		"recipes-index-links-complete": {"Recipes.md", "lists"},
		"recipes-count-drift":          {"Recipes.md", "counts"},
	}
	for _, g := range valid {
		t.Run(g, func(t *testing.T) {
			for rule, key := range listKeys {
				_, err := lint.Run(ix, prof,
					map[string]map[string]any{rule: {key: []any{g}}}, []string{rule})
				require.NoErrorf(t, err, "%s must accept the valid glob %q", rule, g)
			}
			for rule, fk := range scalarKeys {
				_, err := lint.Run(ix, prof,
					map[string]map[string]any{rule: {"file": fk[0], fk[1]: g}}, []string{rule})
				require.NoErrorf(t, err, "%s must accept the valid glob %q", rule, g)
			}
		})
	}
}

// Acceptance is not enough: a glob that passes validation must still select
// the files it names. Without this, validGlobs could be satisfied by a check
// that quietly normalized the pattern into something inert.
func TestAcceptedGlobsStillSelectTheirFiles(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":         "x\n",
		"Areas/Personal/junk.txt":        "x\n",
		"Areas/Personal/Deep/deeper.txt": "x\n",
		"Projects/Snyk/other.txt":        "x\n",
	})
	for _, tc := range []struct {
		glob string
		want int
	}{
		{"Areas/**", 2},                       // both Areas non-markdown files
		{"Areas/*/*", 1},                      // one level down only, so not Deep/
		{"{Areas,Projects}/**", 3},            // alternation reaches both trees
		{"Areas/Personal/[Dd]eep/**", 1},      // character class
		{"Areas/Personal/Deep/deeper.txt", 1}, // literal
		{"Nowhere/**", 0},
	} {
		t.Run(tc.glob, func(t *testing.T) {
			fs, err := lint.Run(ix, prof,
				map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{tc.glob}}},
				[]string{"non-markdown-in-note-folders"})
			require.NoError(t, err)
			require.Lenf(t, fs, tc.want, "glob %q selected %+v", tc.glob, fs)
		})
	}
}

// Overrides reach the engine as []any from TOML but as []string from Go
// callers; the glob check must cover both, or it has a bypass. (The junk
// -pattern and warning_types twins of this test already exist; globs did not
// have one.)
func TestMalformedGlobRejectedInBothConfigSliceShapes(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for name, scopes := range map[string]any{
		"[]any":    []any{"[unclosed"},
		"[]string": []string{"[unclosed"},
	} {
		_, err := lint.Run(ix, prof,
			map[string]map[string]any{"orphan-notes": {"scopes": scopes}}, []string{"orphan-notes"})
		require.Errorf(t, err, "%s config shape must be glob-validated too", name)
		require.Contains(t, err.Error(), "[unclosed")
	}
}

// Validation cannot stop at the first glob it likes: a bad pattern anywhere
// in the list must fail the run.
func TestMalformedGlobRejectedAnywhereInTheList(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	for _, scopes := range [][]any{
		{"[unclosed", "Areas/**"},
		{"Areas/**", "[unclosed"},
		{"Areas/**", "[unclosed", "People/**"},
	} {
		_, err := lint.Run(ix, prof,
			map[string]map[string]any{"orphan-notes": {"scopes": scopes}}, []string{"orphan-notes"})
		require.Errorf(t, err, "list %v must be rejected", scopes)
		require.Contains(t, err.Error(), "[unclosed")
	}
}

// A malformed glob must fail the run even when the rule would report nothing
// anyway. An empty vault is the "nothing to do" branch, and a gate that goes
// green because it had nothing to check proves nothing.
func TestMalformedGlobFailsClosedOnAnEmptyVault(t *testing.T) {
	ix, prof := buildVaultWith(t, "rdegges", map[string]string{})
	for rule, cfg := range map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{"[unclosed"}},
		"orphan-notes":                 {"scopes": []any{"[unclosed"}},
		"recipes-count-drift":          {"file": "Recipes.md", "counts": "[unclosed"},
		"recipes-index-links-complete": {"file": "Recipes.md", "lists": "[unclosed"},
	} {
		t.Run(rule, func(t *testing.T) {
			_, err := lint.Run(ix, prof, map[string]map[string]any{rule: cfg}, []string{rule})
			require.Error(t, err, "%s must fail closed with nothing to check", rule)
		})
	}
}

// Every malformed shape doublestar refuses must be refused at construction,
// with the offending pattern in the message so the user can find it.
func TestEveryMalformedGlobShapeIsRejected(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	malformed := []string{
		"[unclosed", // unterminated class
		"[a-",       // unterminated range
		"[^",        // unterminated negation (caret)
		"[!",        // unterminated negation (bang)
		"[]",        // empty class
		"a[]b",      // empty class mid-pattern
		"[]]",       // class opening on its own terminator
		"Areas/[",   // bad class after a separator
		`Areas\`,    // trailing escape
		`[\]`,       // escape swallows the class terminator
		"{a,b",      // unclosed alternation
		"}",         // alternation close with no open
		"Areas/{a",  // unclosed alternation mid-path
	}
	for _, g := range malformed {
		t.Run(g, func(t *testing.T) {
			require.Falsef(t, doublestar.ValidatePattern(g),
				"premise: doublestar must reject %q", g)
			_, err := lint.Run(ix, prof,
				map[string]map[string]any{"orphan-notes": {"scopes": []any{g}}},
				[]string{"orphan-notes"})
			require.Errorf(t, err, "malformed glob %q must fail the run", g)
			// The message quotes the pattern (%q), so a glob containing a
			// backslash appears escaped — compare against that rendering.
			require.Contains(t, err.Error(), strconv.Quote(g),
				"the error must name the offending pattern")
			require.Contains(t, err.Error(), "orphan-notes", "the error must name the rule")
		})
	}
}
