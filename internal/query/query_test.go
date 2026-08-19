package query

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

func fixture(t *testing.T) (*vault.Index, *profile.Profile) {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"Projects/Snyk/Alpha.md": `---
type: project
category: Snyk
status: active
created: 2026-01-01
updated: 2026-01-01
description: alpha
tags: [go, cli]
---
Alpha builds the THING.
`,
		"Projects/Snyk/Beta.md": `---
type: project
category: Snyk
status: inactive
created: 2026-01-01
updated: 2026-01-01
description: beta
---
Beta was an experiment. See [[Alpha]].
`,
		"People/Snyk/Jane Doe.md": `---
last_met: 2026-05-06
meeting_count: 3
topics: [go]
---
Works on [[Alpha]].
`,
		"Resources/Personal/Loner.md": "---\ntype: resource\n---\nNothing links here.\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	return ix, prof
}

func mustRun(t *testing.T, ix *vault.Index, prof *profile.Profile, opts Options) []Result {
	t.Helper()
	rs, err := Run(ix, prof, opts)
	require.NoError(t, err)
	return rs
}

func paths(rs []Result) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Path)
	}
	return out
}

func TestWhereEquality(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Where: map[string]string{"status": "active"}})
	require.Equal(t, []string{"Projects/Snyk/Alpha.md"}, paths(rs))
}

func TestWhereListContains(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Where: map[string]string{"tags": "cli"}})
	require.Equal(t, []string{"Projects/Snyk/Alpha.md"}, paths(rs))
}

func TestWhereIntAndAND(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Where: map[string]string{"meeting_count": "3", "last_met": "2026-05-06"}})
	require.Equal(t, []string{"People/Snyk/Jane Doe.md"}, paths(rs))
	rs = mustRun(t, ix, prof, Options{Where: map[string]string{"meeting_count": "3", "last_met": "1999-01-01"}})
	require.Empty(t, rs, "predicates AND-combine")
}

func TestTypeFilter(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Type: "person"})
	require.Equal(t, []string{"People/Snyk/Jane Doe.md"}, paths(rs))
}

func TestTextSearch(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Text: "the thing"})
	require.Equal(t, []string{"Projects/Snyk/Alpha.md"}, paths(rs), "case-insensitive")
}

func TestBacklinks(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Backlinks: "Projects/Snyk/Alpha.md"})
	require.Equal(t, []string{"People/Snyk/Jane Doe.md", "Projects/Snyk/Beta.md"}, paths(rs))
}

func TestOrphans(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Orphans: true, Type: "resource-generic"})
	require.Equal(t, []string{"Resources/Personal/Loner.md"}, paths(rs))
}

func TestFrontmatterInResults(t *testing.T) {
	ix, prof := fixture(t)
	rs := mustRun(t, ix, prof, Options{Where: map[string]string{"status": "active"}})
	require.Len(t, rs, 1)
	require.Equal(t, "alpha", rs[0].Frontmatter["description"])
}
