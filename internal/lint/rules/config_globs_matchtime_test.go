package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// Round 4 of #30 settled the design: construction validates syntax
// (ValidatePattern), and match time uses doublestar.MatchUnvalidated — the
// library's documented pairing for pre-validated patterns. An accepted glob
// therefore always evaluates. These tests are the flipped round-3 finding
// pins: they proved the match-time abort fired on valid config and depended
// on vault contents; they now prove neither can happen.

// "{[}]}" and friends: ValidatePattern accepts, Match (with its
// partial-suffix re-validation) refuses, MatchUnvalidated evaluates.
const divergentGlob = "{[}]}"

// A config verdict must come from the config alone, never from what the
// vault happens to contain. The divergent glob evaluates identically on a
// vault with a candidate file, a markdown-only vault, and an empty vault —
// no run errors, findings determined purely by what the pattern matches.
func TestDivergentGlobEvaluatesIdenticallyOnEveryVault(t *testing.T) {
	require.True(t, doublestar.ValidatePattern(divergentGlob),
		"premise: construction accepts this pattern")

	over := map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{divergentGlob}},
	}
	expectMatch := doublestar.MatchUnvalidated(divergentGlob, "Areas/Personal/junk.txt")

	withCandidate, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/junk.txt": "x\n",
	})
	fs, err := lint.Run(withCandidate, prof, over, []string{"non-markdown-in-note-folders"})
	require.NoError(t, err, "an accepted glob must evaluate, never abort the run")
	if expectMatch {
		require.Len(t, fs, 1, "%+v", fs)
	} else {
		require.Empty(t, fs, "%+v", fs)
	}

	mdOnly, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md":  "x\n",
		"Areas/Personal/other.md": "x\n",
	})
	fs, err = lint.Run(mdOnly, prof, over, []string{"non-markdown-in-note-folders"})
	require.NoError(t, err, "the same config must evaluate on a markdown-only vault")
	require.Empty(t, fs, "%+v", fs)

	empty, prof := buildVaultWith(t, "rdegges", map[string]string{})
	for rule, cfg := range map[string]map[string]any{
		"non-markdown-in-note-folders": {"scopes": []any{divergentGlob}},
		"orphan-notes":                 {"scopes": []any{divergentGlob}},
		"recipes-count-drift":          {"file": "Recipes.md", "counts": divergentGlob},
		"recipes-index-links-complete": {"file": "Recipes.md", "lists": divergentGlob},
	} {
		t.Run(rule, func(t *testing.T) {
			_, err := lint.Run(empty, prof, map[string]map[string]any{rule: cfg}, []string{rule})
			require.NoError(t, err, "%s must evaluate the accepted glob on an empty vault too", rule)
		})
	}
}

// The round-3 plumbing turned a doublestar library artifact into a hard
// exit-2 on VALID config: a pattern ValidatePattern accepts and that
// provably designates a real file (MatchUnvalidated says so) aborted the
// run, and the finding the scope was written to catch was never reported.
// Flipped: the finding IS reported.
func TestValidatedGlobThatDesignatesAFileReportsItsFinding(t *testing.T) {
	const junk = "Areas/Personal/junk.txt"
	// The literal path plus an alternation of "one comma" or "nothing", so
	// the pattern designates exactly that one file.
	const glob = junk + "{[,],}"

	require.True(t, doublestar.ValidatePattern(glob),
		"premise: the construction gate accepts this pattern")
	require.True(t, doublestar.MatchUnvalidated(glob, junk),
		"premise: the pattern really does designate %q", junk)

	ix, prof := buildVaultWith(t, "rdegges", map[string]string{
		"Areas/Personal/note.md": "x\n",
		junk:                     "x\n",
	})
	fs, err := lint.Run(ix, prof,
		map[string]map[string]any{"non-markdown-in-note-folders": {"scopes": []any{glob}}},
		[]string{"non-markdown-in-note-folders"})
	require.NoError(t, err, "a validated pattern that names a real file must not abort the run")
	require.Lenf(t, fs, 1, "the scope must select the file it designates: %+v", fs)
	require.Equal(t, junk, fs[0].Path)
}

// ---- the profile -> lint bridge (lint.Context.TypeOf) ---------------------

// divergentScopeVault builds a vault on a profile whose only type scope is
// the divergent glob. The profile loads (the load gate is syntax only) and
// must then classify deterministically through MatchUnvalidated.
func divergentScopeVault(t *testing.T) (*vault.Index, *profile.Profile) {
	t.Helper()
	dir := t.TempDir()
	manifest := "schema_version = 1\nname = \"divergent\"\nscaffold = [\"Notes\"]\n\n" +
		"[[types]]\nname = \"note\"\nscope = [\"" + divergentGlob + "\"]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err, "premise: syntax-valid scopes load")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Notes", "A.md"),
		[]byte("---\nmeeting_count: 1\nlast_met: 2026-01-02\n---\nbody\n"), 0o644))
	ix, err := vault.BuildIndex(root, vault.WalkOptions{})
	require.NoError(t, err)
	return ix, prof
}

// A divergent profile scope classifies deterministically — the same verdict
// MatchUnvalidated gives — and a full run over the vault completes. Flipped
// from the round-3 pins that asserted the run aborted here.
func TestDivergentProfileScopeClassifiesDeterministically(t *testing.T) {
	ix, prof := divergentScopeVault(t)

	want := ""
	if doublestar.MatchUnvalidated(divergentGlob, "Notes/A.md") {
		want = "note"
	}
	got := prof.TypeOf("Notes/A.md", nil)
	require.Equal(t, want, got, "classification must follow MatchUnvalidated exactly")

	// A type-dispatching rule and a default full run both complete.
	_, err := lint.Run(ix, prof, map[string]map[string]any{
		"frontmatter-key-order": {
			"enabled": true,
			"orders":  map[string]any{"note": []any{"last_met", "meeting_count"}},
		},
	}, []string{"frontmatter-key-order"})
	require.NoError(t, err)
	_, err = lint.Run(ix, prof, nil, nil)
	require.NoError(t, err, "a full run over a divergent-scope profile must complete")
}
