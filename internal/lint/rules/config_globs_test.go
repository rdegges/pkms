package rules_test

import (
	"strconv"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
)

// The #30 fix makes validGlobs the sole gate in front of matchAnyGlob, which
// now drops doublestar's error entirely. These tests hold that gate to the
// two things it must be: complete (nothing it accepts can error at match
// time) and non-destructive (it accepts every glob shape real config uses,
// and those globs still match).

// globNames is the corpus of vault-relative paths a scope glob is matched
// against at check time — real shapes plus hostile ones.
var globNames = []string{
	"", "Now.md", "Areas/Personal/x.md", "Areas/Personal/Sub/deep.md",
	"Meetings/Snyk/2026/05/06/1100 - Weekly Sync.md",
	"Resources/Personal/Recipes/Café au Lait.md",
	"Projects/Snyk/a.md", "attachments/img.png", "a/b/c/d/e/f/g",
	"[brackets].md", "back\\slash.md", "{braces}.md", "\x00", "a\nb.md",
	"конспект.md", "x", "/", "//", "./x", "../x",
}

// The invariant behind matchAnyGlob's dropped error: any pattern the rule
// factories accept must be safe to feed to doublestar.Match at check time,
// for every path. A divergence here means the pattern silently matches
// nothing in production — the exact bug #30 set out to fix, reopened.
func FuzzAcceptedScopeGlobIsMatchable(f *testing.F) {
	for _, seed := range []string{
		"Areas/**", "**/*.md", "Resources/{Snyk,Personal}/*.md",
		"Meetings/*/[0-9][0-9][0-9][0-9]/[0-9][0-9]/[0-9][0-9]/*.md",
		"[unclosed", "[a-", "[]", "[^", "[!", "{a,b", "}", "{a,{b,c}}",
		"{}", "{,}", "[z-a]", "[--]", "[a-]", "[[]", "[]]", `a\`, `\`,
		"**", "a**b", "*", "?", "[^]a]", "{a/b,c}", "café*", "\x00",
		"{{}}", "{a,b}{c,d}", "[\\]", "**/**/**",
		// Known divergences this fuzzer found: doublestar validates a
		// pattern as a whole, but when matching stops early it re-validates
		// the pattern SUFFIX from that point — and a suffix that splits a
		// character class holding '{' or '}' does not validate.
		"{[}]}", "[!a{b]", "[!00A{000]",
	} {
		f.Add(seed)
	}
	ix, prof := buildVaultWith(f, "rdegges", map[string]string{
		"Areas/Personal/n.md":  "x\n",
		"Areas/Personal/j.txt": "x\n",
	})

	f.Fuzz(func(t *testing.T, pattern string) {
		over := map[string]map[string]any{
			"non-markdown-in-note-folders": {"scopes": []any{pattern}},
		}
		if _, err := lint.Run(ix, prof, over, []string{"non-markdown-in-note-folders"}); err != nil {
			return // rejected up front: fail-closed, nothing left to prove
		}
		for _, n := range globNames {
			if _, err := doublestar.Match(pattern, n); err != nil {
				t.Fatalf("glob %q was accepted but doublestar.Match(%q, %q) fails: %v — "+
					"matchAnyGlob drops that error, so the scope silently matches nothing",
					pattern, pattern, n, err)
			}
		}
	})
}

// The concrete, minimal statement of the same defect the fuzzer found, at
// the rule-config layer. validGlobs uses doublestar.ValidatePattern, but
// doublestar.Match re-validates the pattern SUFFIX where matching stopped,
// and a suffix that splits a character class holding '{' or '}' is not
// valid. So Match errors on a pattern validGlobs accepted — and matchAnyGlob
// now discards that error, which is the silent "matches nothing" #30 closed.
func TestValidatedGlobMustNotErrorAtMatchTime(t *testing.T) {
	ix, prof, _ := buildVault(t, cleanVault())
	// Each entry: a glob validGlobs accepts, and a path Match refuses it on.
	for glob, victim := range map[string]string{
		"{[}]}":      "x",
		"[!a{b]":     "a/b",
		"[!00A{000]": "Areas/Personal/x.md",
	} {
		t.Run(glob, func(t *testing.T) {
			require.True(t, doublestar.ValidatePattern(glob),
				"premise: the construction-time check accepts this pattern")
			_, matchErr := doublestar.Match(glob, victim)
			require.Errorf(t, matchErr,
				"premise: doublestar refuses %q while matching %q", glob, victim)

			_, err := lint.Run(ix, prof,
				map[string]map[string]any{"orphan-notes": {"scopes": []any{glob}}},
				[]string{"orphan-notes"})
			require.Error(t, err,
				"a glob doublestar refuses to match must fail the run; accepting it "+
					"means the scope silently matches nothing at check time")
		})
	}
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
