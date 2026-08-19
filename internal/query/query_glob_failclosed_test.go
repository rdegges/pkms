package query

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// #30's final shape: profile load validates scope-glob syntax, and
// classification matches with doublestar.MatchUnvalidated, so every loaded
// scope evaluates. These pin the query surface of that contract.

// globProfile writes a one-type profile whose scope is `glob` and indexes a
// vault holding `rel`.
func globProfile(t *testing.T, glob, rel string) (*vault.Index, *profile.Profile) {
	t.Helper()
	dir := t.TempDir()
	manifest := "schema_version = 1\nname = \"g\"\nscaffold = [\"Notes\"]\n\n" +
		"[[types]]\nname = \"note\"\nscope = [" + quoteTOML(glob) + "]\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err, "premise: the load-time syntax gate accepts %q", glob)

	root := t.TempDir()
	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte("---\ntitle: x\n---\nbody\n"), 0o644))
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	return ix, prof
}

func quoteTOML(s string) string {
	out := []rune{'"'}
	for _, r := range s {
		if r == '"' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(append(out, '"'))
}

// A divergent-but-valid scope glob ("{[}]}" passes ValidatePattern; Match
// refuses it on its suffix re-validation) classifies deterministically: the
// query answers exactly what MatchUnvalidated says, on every predicate
// combination that reaches TypeOf.
func TestDivergentScopeGlobClassifiesDeterministically(t *testing.T) {
	const glob = "{[}]}"
	require.True(t, doublestar.ValidatePattern(glob))
	_, matchErr := doublestar.Match(glob, "Notes/A.md")
	require.Error(t, matchErr, "premise: Match refuses this pattern (suffix re-validation)")

	ix, prof := globProfile(t, glob, "Notes/A.md")
	want := 0
	if doublestar.MatchUnvalidated(glob, "Notes/A.md") {
		want = 1
	}
	for label, opts := range map[string]Options{
		"type only":      {Type: "note"},
		"type + where":   {Type: "note", Where: map[string]string{"title": "x"}},
		"type + text":    {Type: "note", Text: "body"},
		"type + orphans": {Type: "note", Orphans: true},
		"no type":        {},
	} {
		t.Run(label, func(t *testing.T) {
			rs := Run(ix, prof, opts)
			if label == "no type" {
				require.Len(t, rs, 1, "no type predicate means no classification")
				return
			}
			require.Lenf(t, rs, want,
				"classification must follow MatchUnvalidated exactly: %+v", rs)
		})
	}
}

// A glob that is simply a no-match stays a clean empty result — the ordinary
// "nothing matched" answer.
func TestRunReturnsCleanEmptyForAScopeThatSimplyMatchesNothing(t *testing.T) {
	ix, prof := globProfile(t, "Elsewhere/**", "Notes/A.md")
	rs := Run(ix, prof, Options{Type: "note"})
	require.Empty(t, rs)
}
