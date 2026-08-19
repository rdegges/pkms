package query_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/query"
	"github.com/rdegges/pkms/internal/vault"
	"github.com/stretchr/testify/require"
)

func buildQueryVault(t *testing.T, files map[string]string) (*vault.Index, *profile.Profile) {
	t.Helper()
	root := t.TempDir()
	for rel, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}

	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	require.NotNil(t, prof)

	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	require.NotNil(t, ix)
	return ix, prof
}

func mustQueryRun(t *testing.T, ix *vault.Index, prof *profile.Profile, opts query.Options) []query.Result {
	t.Helper()
	results, err := query.Run(ix, prof, opts)
	require.NoError(t, err)
	return results
}

func resultPaths(results []query.Result) []string {
	paths := make([]string, len(results))
	for i, result := range results {
		paths[i] = result.Path
	}
	return paths
}

func TestRunSortsResultsByAscendingPath(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"z-last.md":        "body\n",
		"a-first.md":       "body\n",
		"Folder/middle.md": "body\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{})
	require.Equal(t, []string{"Folder/middle.md", "a-first.md", "z-last.md"}, resultPaths(results))
}

func TestRunWhereMatchesScalarIntegerAndContainedListValues(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Integer.md": "---\nmeeting_count: 3\n---\nbody\n",
		"Lists.md":   "---\ntags: [alpha, beta]\ncounts: [2, 3]\n---\nbody\n",
		"Scalar.md":  "---\nstatus: active\n---\nbody\n",
		"NoFM.md":    "status: active in the body\n",
	})

	tests := []struct {
		name string
		key  string
		want string
		path string
	}{
		{name: "integer uses base ten", key: "meeting_count", want: "3", path: "Integer.md"},
		{name: "string list contains", key: "tags", want: "beta", path: "Lists.md"},
		{name: "integer list contains", key: "counts", want: "3", path: "Lists.md"},
		{name: "scalar equality", key: "status", want: "active", path: "Scalar.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := mustQueryRun(t, ix, prof, query.Options{Where: map[string]string{tt.key: tt.want}})
			require.Equal(t, []string{tt.path}, resultPaths(results))
		})
	}

	require.Empty(t, mustQueryRun(t, ix, prof, query.Options{Where: map[string]string{"status": "missing"}}))
}

func TestRunWhereSupportsDottedNestedKeysAndANDsPredicates(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Both.md":     "---\na:\n  b: value\nstatus: active\n---\nbody\n",
		"Nested.md":   "---\na:\n  b: value\nstatus: inactive\n---\nbody\n",
		"TopLevel.md": "---\na.b: value\nstatus: active\n---\nbody\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{Where: map[string]string{
		"a.b":    "value",
		"status": "active",
	}})
	require.Equal(t, []string{"Both.md"}, resultPaths(results))
}

func TestRunTextIsCaseInsensitiveAndSearchesBodyOnly(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Body.md":        "---\ntitle: ordinary\n---\nThe HaYsTaCk is in this BODY.\n",
		"Frontmatter.md": "---\ntitle: haystack only here\n---\nNothing relevant below.\n",
		"NoMatch.md":     "Completely unrelated.\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{Text: "hAyStAcK"})
	require.Equal(t, []string{"Body.md"}, resultPaths(results))
}

func TestRunBacklinksRequiresResolvedLinkToExactPath(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Projects/Snyk/Target.md":     "# Target\n",
		"Projects/Personal/Target.md": "# A duplicate basename\n",
		"Sources/Exact.md":            "[[Projects/Snyk/Target]]\n",
		"Sources/AmbiguousBare.md":    "[[Target]]\n",
		"Sources/Different.md":        "[[Projects/Personal/Target]]\n",
		"Sources/Broken.md":           "[[Missing]]\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{Backlinks: "Projects/Snyk/Target.md"})
	require.Equal(t, []string{"Sources/AmbiguousBare.md", "Sources/Exact.md"}, resultPaths(results))
}

func TestRunOrphansIgnoresSelfLinksAndCountsInboundLinksFromOtherNotes(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"A.md": "[[A]]\n",
		"B.md": "body\n",
		"C.md": "[[C]] and [[B]]\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{Orphans: true})
	require.Equal(t, []string{"A.md", "C.md"}, resultPaths(results))
}

func TestRunANDCombinesAllOptionPredicates(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Targets/Goal.md":                 "# Goal\n",
		"Targets/Other.md":                "# Other\n",
		"Projects/Snyk/Match.md":          "---\nstatus: active\n---\nThe NEEDLE. [[Targets/Goal]]\n",
		"Projects/Snyk/WrongLink.md":      "---\nstatus: active\n---\nThe needle. [[Targets/Other]]\n",
		"Projects/Personal/WrongWhere.md": "---\nstatus: paused\n---\nThe needle. [[Targets/Goal]]\n",
		"People/Snyk/WrongType.md":        "---\nstatus: active\n---\nThe needle. [[Targets/Goal]]\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{
		Type:      "project",
		Where:     map[string]string{"status": "active"},
		Text:      "needle",
		Backlinks: "Targets/Goal.md",
		Orphans:   true,
	})
	require.Equal(t, []string{"Projects/Snyk/Match.md"}, resultPaths(results))
}

func TestRunUsesRdeggesProfileTypes(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"People/Snyk/Alice.md":                       "body\n",
		"People/Personal/Bob.md":                     "body\n",
		"People/Other/NotPerson.md":                  "body\n",
		"People/Snyk/Deep/NotDirect.md":              "body\n",
		"Projects/Snyk/Work.md":                      "body\n",
		"Projects/Personal/Home.md":                  "body\n",
		"Projects/Other/NotProject.md":               "body\n",
		"Resources/Snyk/Generic.md":                  "---\ntitle: Generic\n---\nbody\n",
		"Resources/Personal/Also Generic.md":         "body\n",
		"Resources/Snyk/ClipByURL.md":                "---\nsource_url: https://example.com\n---\nbody\n",
		"Resources/Personal/ClipByDate.md":           "---\ndate_clipped: 2026-07-15\n---\nbody\n",
		"Resources/Personal/ClipByPresentNullKey.md": "---\nsource_url:\n---\nbody\n",
		"Resources/Other/NotAResourceProfileType.md": "body\n",
	})

	tests := []struct {
		typeName string
		want     []string
	}{
		{typeName: "person", want: []string{"People/Personal/Bob.md", "People/Snyk/Alice.md"}},
		{typeName: "project", want: []string{"Projects/Personal/Home.md", "Projects/Snyk/Work.md"}},
		{typeName: "resource-generic", want: []string{"Resources/Personal/Also Generic.md", "Resources/Snyk/Generic.md"}},
		{typeName: "clip-summary", want: []string{"Resources/Personal/ClipByDate.md", "Resources/Personal/ClipByPresentNullKey.md", "Resources/Snyk/ClipByURL.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			results := mustQueryRun(t, ix, prof, query.Options{Type: tt.typeName})
			require.Equal(t, tt.want, resultPaths(results))
		})
	}
}

func TestRunResultCarriesParsedFrontmatter(t *testing.T) {
	t.Parallel()

	ix, prof := buildQueryVault(t, map[string]string{
		"Metadata.md": "---\ntitle: Example\ndate: 2026-07-15\nmeeting_count: 3\n---\nbody\n",
	})

	results := mustQueryRun(t, ix, prof, query.Options{})
	require.Len(t, results, 1)
	require.Equal(t, "Metadata.md", results[0].Path)
	require.Equal(t, map[string]any{
		"title":         "Example",
		"date":          "2026-07-15",
		"meeting_count": int64(3),
	}, results[0].Frontmatter)
}
