package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// SPEC §37: one generic `index-complete` rule enforces every [[indexes]]
// declaration, replacing the three per-index rule ids. The contracts come
// from the profile (pre-validated at load, §36); the rule reads nothing
// from its own config table.

// The rdegges Projects.md contract: an unlinked project is a finding at
// the declared warning severity. Message format unchanged from the old
// rules (the parity ruling depends on it).
func TestIndexCompleteEnforcesTheProjectsContract(t *testing.T) {
	files := cleanVault()
	files["Projects.md"] = "# Projects\n- [[Cataloged]]\n"
	files["Projects/Snyk/Cataloged.md"] = "---\ntype: project\ncategory: Snyk\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	files["Projects/Snyk/Uncataloged.md"] = "---\ntype: project\ncategory: Snyk\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	ix, prof, _ := buildVault(t, files)

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	var hits []lint.Finding
	for _, f := range fs {
		if f.Path == "Projects/Snyk/Uncataloged.md" {
			hits = append(hits, f)
		}
	}
	require.Len(t, hits, 1, "%+v", fs)
	require.Equal(t, "index-complete", hits[0].Rule)
	require.Equal(t, lint.Warning, hits[0].Severity, "the Projects.md entry declares warning")
	require.Equal(t, "not cataloged in Projects.md", hits[0].Message)
}

// The recipes contract carries error severity and both directions:
// an unlisted recipe is a finding, and a listed link that resolves to
// nothing is a finding (must-link-all-and-resolve).
func TestIndexCompleteEnforcesTheRecipesContractBothWays(t *testing.T) {
	files := cleanVault()
	files["Resources/Personal/Recipes/Recipes.md"] = "---\nrecipe_count: 1\n---\n# Recipes\n- [[Ghost Stew]]\n"
	files["Resources/Personal/Recipes/Unlisted.md"] = "---\ntitle: Unlisted\n---\nbody\n"
	ix, prof, _ := buildVault(t, files)

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	var unlisted, ghost *lint.Finding
	for i := range fs {
		switch {
		case fs[i].Path == "Resources/Personal/Recipes/Unlisted.md":
			unlisted = &fs[i]
		case fs[i].Message == "index links [[Ghost Stew]] but no such note exists":
			ghost = &fs[i]
		}
	}
	require.NotNil(t, unlisted, "the unlisted recipe must be a finding: %+v", fs)
	require.Equal(t, lint.Error, unlisted.Severity, "the recipes entry declares error")
	require.Equal(t, "not cataloged in Resources/Personal/Recipes/Recipes.md", unlisted.Message)
	require.NotNil(t, ghost, "the unresolvable index link must be a finding: %+v", fs)
	require.Equal(t, lint.Error, ghost.Severity)
}

// A missing index file with notes to catalog is itself the finding, at the
// entry's severity.
func TestIndexCompleteFlagsAMissingIndexFile(t *testing.T) {
	files := cleanVault()
	delete(files, "Projects.md")
	files["Projects/Snyk/Orphaned.md"] = "---\ntype: project\ncategory: Snyk\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	ix, prof, _ := buildVault(t, files)

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	var hit *lint.Finding
	for i := range fs {
		if fs[i].Path == "Projects.md" {
			hit = &fs[i]
		}
	}
	require.NotNil(t, hit, "%+v", fs)
	require.Contains(t, hit.Message, "index file missing")
	require.Equal(t, lint.Warning, hit.Severity)
}

// Zero [[indexes]] entries = zero contracts declared = pass. A decided
// posture, stated in §37: the para profile hits this branch on day one.
func TestIndexCompleteIsVacuouslyGreenWithNoDeclarations(t *testing.T) {
	prof, err := profile.Load("para")
	require.NoError(t, err)
	require.Empty(t, prof.Indexes, "premise: para declares no index contracts")

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Resources"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Resources", "n.md"), []byte("x\n"), 0o644))
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	require.Empty(t, fs, "%+v", fs)
}

// The migration surface (SPEC §35 doing its job): a profile still carrying
// one of the retired per-index [lint.*] tables fails loudly at the
// unknown-id gate instead of silently configuring nothing.
func TestRetiredIndexRuleTablesFailTheUnknownIdGate(t *testing.T) {
	for _, retired := range []string{
		"resources-cataloged-in-index",
		"projects-linked-from-master",
		"recipes-index-links-complete",
	} {
		t.Run(retired, func(t *testing.T) {
			dir := t.TempDir()
			manifest := "schema_version = 1\nname = \"old\"\nscaffold = [\"Notes\"]\n\n" +
				"[[types]]\nname = \"note\"\nscope = [\"Notes/**\"]\n\n" +
				"[lint." + retired + "]\nfile = \"index.md\"\nlists = \"Notes/*.md\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(manifest), 0o644))
			prof, err := profile.Load(dir)
			require.NoError(t, err, "profiles load; the lint engine owns rule-id validation")

			root := t.TempDir()
			ix, err := vault.BuildIndex(root, vault.WalkOptions{})
			require.NoError(t, err)
			_, err = lint.Run(ix, prof, nil, nil)
			require.Error(t, err, "a retired rule id must fail loudly (§35), not configure nothing")
			require.Contains(t, err.Error(), retired)
		})
	}
}
