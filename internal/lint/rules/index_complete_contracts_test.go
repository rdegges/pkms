package rules_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
)

// Companion to index_complete_test.go. Those tests pin one contract at a
// time through `--rules index-complete`. These pin the properties that only
// show up across contracts and across the whole run: that a DEFAULT run
// enforces every shipped [[indexes]] entry with no [lint.*] table to switch
// it on, that each policy means what it says, and that the coarse
// granularity §37 accepted (all-or-nothing enable/severity) is the
// granularity actually shipped.

// onlyIndexComplete narrows a run's findings to the rule under test, so
// these tests can use unfiltered runs without asserting on unrelated rules.
func onlyIndexComplete(fs []lint.Finding) []lint.Finding {
	var out []lint.Finding
	for _, f := range fs {
		if f.Rule == "index-complete" {
			out = append(out, f)
		}
	}
	return out
}

// violatedContractsVault is a rdegges-shaped vault that breaks every shipped
// index contract: each index file exists and is empty of links, and an
// in-scope note sits under EACH BRANCH of each contract's `lists` glob. Both
// alternation branches are populated on purpose — with only the Snyk side
// present, narrowing `Resources/{Snyk,Personal}/*.md` to `Resources/Snyk/*.md`
// silently drops half the vault from enforcement and no test notices.
func violatedContractsVault() map[string]string {
	const project = "---\ntype: project\ncategory: %s\nstatus: active\ncreated: 2026-01-01\nupdated: 2026-01-01\ndescription: x\n---\nx\n"
	files := cleanVault()
	files["Projects.md"] = "# Projects\n"
	files["index.md"] = "# Index\n"
	files["Resources/Personal/Recipes/Recipes.md"] = "---\nrecipe_count: 1\n---\n# Recipes\n"
	files["Projects/Snyk/Unlinked Snyk Project.md"] = fmt.Sprintf(project, "Snyk")
	files["Projects/Personal/Unlinked Personal Project.md"] = fmt.Sprintf(project, "Personal")
	files["Resources/Snyk/Unlisted Snyk Resource.md"] = "---\ntitle: Unlisted Snyk Resource\n---\nbody\n"
	files["Resources/Personal/Unlisted Personal Resource.md"] = "---\ntitle: Unlisted Personal Resource\n---\nbody\n"
	files["Resources/Personal/Recipes/Soup.md"] = "---\ntitle: Soup\n---\nbody\n"
	return files
}

// The load-bearing property of §37, and the one the retired mirror test used
// to protect from the other side: enforcement now derives ONLY from
// [[indexes]], so a declaration silently dropped from the shipped profile
// drops a check with it. A default run (no --rules, no [lint.index-complete]
// table anywhere) must produce one finding per shipped contract, each at its
// own declared severity and with the pre-§37 message text.
//
// profile/indexes_test.go pins each shipped entry's file/severity/policy, so
// deleting one outright already fails. It does NOT pin `lists`, and no other
// test enforces the index.md contract end to end: narrowing that entry's glob
// from `Resources/{Snyk,Personal}/*.md` to `Resources/Snyk/*.md` drops every
// Personal resource out of enforcement and the rest of the suite stays green
// (measured). That is the hole this test closes.
func TestIndexCompleteEnforcesEveryShippedContractInADefaultRun(t *testing.T) {
	ix, prof, _ := buildVault(t, violatedContractsVault())

	all, err := lint.Run(ix, prof, nil, nil) // no --rules scoping
	require.NoError(t, err)
	got := onlyIndexComplete(all)

	type hit struct {
		path, msg string
		sev       lint.Severity
	}
	var flat []hit
	for _, f := range got {
		flat = append(flat, hit{f.Path, f.Message, f.Severity})
		require.False(t, f.Fixable, "index-complete is report-only (docs/LINT-RULES.md)")
	}
	require.Equal(t, []hit{
		{"Projects/Personal/Unlinked Personal Project.md", "not cataloged in Projects.md", lint.Warning},
		{"Projects/Snyk/Unlinked Snyk Project.md", "not cataloged in Projects.md", lint.Warning},
		{"Resources/Personal/Recipes/Soup.md", "not cataloged in Resources/Personal/Recipes/Recipes.md", lint.Error},
		{"Resources/Personal/Unlisted Personal Resource.md", "not cataloged in index.md", lint.Warning},
		{"Resources/Snyk/Unlisted Snyk Resource.md", "not cataloged in index.md", lint.Warning},
	}, flat, "every shipped [[indexes]] entry enforced, over both branches of "+
		"its lists glob, at its declared severity: %+v", all)
}

// An index file that its own contract's `lists` glob matches must not be
// asked to catalog itself. Recipes.md sits inside Resources/Personal/
// Recipes/*.md, so the self-exclusion is live in the shipped profile — drop
// it and every vault grows a permanent unfixable error.
func TestIndexCompleteNeverAsksAnIndexToCatalogItself(t *testing.T) {
	ix, prof, _ := buildVault(t, violatedContractsVault())

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	for _, f := range fs {
		require.NotEqual(t, "Resources/Personal/Recipes/Recipes.md", f.Path,
			"the index is excluded from its own lists glob: %+v", fs)
	}
}

// The two policies must stay distinguishable. `must-link-all-and-resolve`
// (recipes) reports an index wikilink that resolves to nothing;
// `must-link-all` (Projects.md, index.md) does not. Both directions are
// asserted in ONE run so a regression that made every contract reverse —
// or none of them — cannot pass by being consistent.
func TestIndexCompletePolicyDecidesWhetherIndexLinksMustResolve(t *testing.T) {
	files := cleanVault()
	files["Projects.md"] = "# Projects\n- [[Ghost Project]]\n"
	files["index.md"] = "# Index\n- [[Ghost Resource]]\n"
	files["Resources/Personal/Recipes/Recipes.md"] = "---\nrecipe_count: 0\n---\n# Recipes\n- [[Ghost Recipe]]\n"
	ix, prof, _ := buildVault(t, files)

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)

	require.Equal(t, []lint.Finding{{
		Rule:     "index-complete",
		Severity: lint.Error,
		Path:     "Resources/Personal/Recipes/Recipes.md",
		Line:     5,
		Message:  "index links [[Ghost Recipe]] but no such note exists",
	}}, fs, "only the must-link-all-and-resolve contract reports unresolvable "+
		"index links; the two must-link-all contracts carry one each and stay quiet: %+v", fs)
}

// §37 states the granularity loss out loud: a rule-level `severity` override
// flattens EVERY entry's declared severity. Prose is not a gate — pin it, in
// both directions, so the loss stays a decision rather than becoming a
// surprise. The premise assertion is what makes this a real test: the
// un-overridden run must genuinely mix severities.
func TestIndexCompleteSeverityOverrideFlattensEveryContract(t *testing.T) {
	ix, prof, _ := buildVault(t, violatedContractsVault())

	base, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	require.Equal(t, map[lint.Severity]int{lint.Warning: 4, lint.Error: 1}, severities(base),
		"premise: the declared severities differ across contracts: %+v", base)

	for _, want := range []lint.Severity{lint.Error, lint.Warning} {
		t.Run(string(want), func(t *testing.T) {
			fs, err := lint.Run(ix, prof, map[string]map[string]any{
				"index-complete": {"severity": string(want)},
			}, []string{"index-complete"})
			require.NoError(t, err)
			require.Len(t, fs, len(base), "the override changes severity, not which contracts run")
			require.Equal(t, map[lint.Severity]int{want: len(base)}, severities(fs),
				"a rule-level severity override flattens every declared severity (SPEC §37): %+v", fs)
		})
	}
}

// The other half of the coarse granularity: `enabled = false` is all-or-
// nothing. It must switch off every contract — including the error-severity
// recipes one — and it must do so on an unfiltered run, not just when the
// rule is named.
func TestIndexCompleteDisabledTurnsOffEveryContract(t *testing.T) {
	ix, prof, _ := buildVault(t, violatedContractsVault())
	off := map[string]map[string]any{"index-complete": {"enabled": false}}

	for _, only := range [][]string{nil, {"index-complete"}} {
		fs, err := lint.Run(ix, prof, off, only)
		require.NoError(t, err)
		require.Emptyf(t, onlyIndexComplete(fs),
			"enabled = false must silence every contract (--rules %v): %+v", only, fs)
	}
}

// indexesProfile writes a minimal on-disk profile carrying the given
// [[indexes]] TOML body, so the generic rule can be exercised beyond the
// three contracts the rdegges profile happens to ship.
func indexesProfile(t *testing.T, entries string) *profile.Profile {
	t.Helper()
	dir := t.TempDir()
	src := "schema_version = 1\nname = \"ix\"\nscaffold = [\"Notes\"]\n\n" +
		"[[types]]\nname = \"note\"\nscope = [\"Notes/**\"]\n\n" + entries
	require.NoError(t, os.WriteFile(filepath.Join(dir, "profile.toml"), []byte(src), 0o644))
	prof, err := profile.Load(dir)
	require.NoError(t, err)
	return prof
}

func indexesVault(t *testing.T, prof *profile.Profile, files map[string]string) *vault.Index {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	return ix
}

// The rule is generic, so a note in scope of TWO contracts owes a link to
// both — one finding each, each naming its own index and carrying its own
// severity. Three entries also exercise the accumulation loop past the
// shipped profile's shape. Map iteration order is not involved, but the
// findings come from a slice walk feeding a sort, so the run is repeated:
// an unstable order here would surface as a flaky diff for every consumer.
func TestIndexCompleteAccumulatesOverlappingContractsIndependently(t *testing.T) {
	prof := indexesProfile(t, `
[[indexes]]
file = "A.md"
lists = "Notes/*.md"
policy = "must-link-all"
severity = "warning"

[[indexes]]
file = "B.md"
lists = "Notes/Shared.md"
policy = "must-link-all"
severity = "error"

[[indexes]]
file = "C.md"
lists = "Notes/*.md"
policy = "must-link-all-and-resolve"
severity = "error"
`)
	ix := indexesVault(t, prof, map[string]string{
		"A.md":             "# A\n",
		"B.md":             "# B\n",
		"C.md":             "# C\n- [[Nowhere]]\n",
		"Notes/Shared.md":  "shared\n",
		"Notes/Private.md": "private\n",
	})

	want := []lint.Finding{
		{Rule: "index-complete", Severity: lint.Error, Path: "C.md", Line: 2,
			Message: "index links [[Nowhere]] but no such note exists"},
		{Rule: "index-complete", Severity: lint.Warning, Path: "Notes/Private.md",
			Message: "not cataloged in A.md"},
		{Rule: "index-complete", Severity: lint.Error, Path: "Notes/Private.md",
			Message: "not cataloged in C.md"},
		{Rule: "index-complete", Severity: lint.Warning, Path: "Notes/Shared.md",
			Message: "not cataloged in A.md"},
		{Rule: "index-complete", Severity: lint.Error, Path: "Notes/Shared.md",
			Message: "not cataloged in B.md"},
		{Rule: "index-complete", Severity: lint.Error, Path: "Notes/Shared.md",
			Message: "not cataloged in C.md"},
	}
	for i := 0; i < 20; i++ {
		fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
		require.NoError(t, err)
		require.Equalf(t, want, fs, "run %d: findings must be complete and stably ordered", i)
	}
}

// A contract whose `lists` glob matches nothing and whose `file` is absent
// is the fresh-vault branch: no index and nothing to catalog is fine. Pinned
// separately from the para vacuous-pass case, which reaches green by
// declaring no contracts at all — this one green-lights a DECLARED contract,
// so the two cannot be collapsed.
func TestIndexCompleteIsSilentWhenADeclaredContractHasNothingToCatalog(t *testing.T) {
	prof := indexesProfile(t, `
[[indexes]]
file = "A.md"
lists = "Notes/*.md"
policy = "must-link-all-and-resolve"
severity = "error"
`)
	ix := indexesVault(t, prof, map[string]string{"Other.md": "unrelated\n"})

	fs, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	require.Empty(t, fs, "no index and nothing to catalog is a fresh vault, not a finding: %+v", fs)
}

// GAP (report, not a promise the change made): `index-complete` reads
// NOTHING from its own [lint.*] table, and the engine does not reject
// unknown keys in a rule table. The natural migration move — rename the
// retired table rather than delete it — therefore lands in the one shape
// §35's loud unknown-id gate cannot see: a registered id whose file/lists
// keys configure nothing, silently. Pinned as OBSERVED behavior. Invert it
// if the engine ever grows unknown-key rejection; do not delete it.
func TestKnownGap_IndexCompleteSilentlyIgnoresStaleFileAndListsKeys(t *testing.T) {
	ix, prof, _ := buildVault(t, violatedContractsVault())

	base, err := lint.Run(ix, prof, nil, []string{"index-complete"})
	require.NoError(t, err)
	require.NotEmpty(t, base, "premise: the shipped contracts report")

	// The shape a user reaches for when migrating off a retired id.
	got, err := lint.Run(ix, prof, map[string]map[string]any{
		"index-complete": {"file": "Projects.md", "lists": "Projects/{Snyk,Personal}/*.md"},
	}, []string{"index-complete"})
	require.NoError(t, err, "GAP: the stale keys are accepted, not rejected")
	require.Equal(t, base, got,
		"GAP: file/lists in [lint.index-complete] configure nothing and are "+
			"never reported — contracts still come only from [[indexes]]: %+v", got)
}
