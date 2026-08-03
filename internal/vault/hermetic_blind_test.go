package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdegges/pkms/internal/profile"
	"github.com/rdegges/pkms/internal/vault"
	"github.com/stretchr/testify/require"
)

func writeVaultFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
	}
}

func buildVault(t *testing.T, files map[string]string) (*vault.Index, *profile.Profile) {
	t.Helper()
	root := t.TempDir()
	writeVaultFiles(t, root, files)

	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	require.NotNil(t, prof)

	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)
	require.NotNil(t, ix)
	return ix, prof
}

func TestParseNoteFrontmatterRequiresExactFenceAtByteZero(t *testing.T) {
	t.Parallel()

	valid := vault.ParseNote("valid.md", []byte("---\ntitle: Valid\n---\nbody\n"))
	require.NotNil(t, valid.FM)
	require.False(t, valid.FM.Unclosed)

	for name, src := range map[string]string{
		"leading blank line": "\n---\ntitle: No\n---\n",
		"leading space":      " ---\ntitle: No\n---\n",
		"trailing space":     "--- \ntitle: No\n---\n",
		"text before fence":  "prefix\n---\ntitle: No\n---\n",
	} {
		t.Run(name, func(t *testing.T) {
			note := vault.ParseNote("note.md", []byte(src))
			require.Nil(t, note.FM)
		})
	}
}

func TestParseNoteFrontmatterClosingFencesMustBeExact(t *testing.T) {
	t.Parallel()

	for name, closing := range map[string]string{
		"hyphen fence": "---",
		"dot fence":    "...",
	} {
		t.Run(name, func(t *testing.T) {
			src := "---\ntitle: Closed\n" + closing + "\nbody\n"
			note := vault.ParseNote("note.md", []byte(src))
			require.NotNil(t, note.FM)
			require.False(t, note.FM.Unclosed)
			require.Equal(t, "body\n", string(note.Body))
		})
	}

	t.Run("non-exact candidate is skipped", func(t *testing.T) {
		note := vault.ParseNote("note.md", []byte("---\ntitle: Still open\n--- \n...\nbody\n"))
		require.NotNil(t, note.FM)
		require.False(t, note.FM.Unclosed)
		require.Equal(t, "body\n", string(note.Body))
	})
}

func TestParseNoteUnclosedFrontmatterHasNilBody(t *testing.T) {
	t.Parallel()

	note := vault.ParseNote("unclosed.md", []byte("---\ntitle: Never closed\nbody text\n"))
	require.NotNil(t, note.FM)
	require.True(t, note.FM.Unclosed)
	require.Nil(t, note.Body)
}

func TestParseNoteFrontmatterYAMLTypesAndStringLists(t *testing.T) {
	t.Parallel()

	src := strings.Join([]string{
		"---",
		"date: 2026-07-15",
		"meeting_count: 3",
		"empty:",
		"nothing: null",
		"bare: single value",
		"list:",
		"  - first",
		"  - second",
		"empty_list: []",
		"mixed:",
		"  - text",
		"  - 2",
		"numbers: [1, 2]",
		"mapping:",
		"  child: 4",
		"---",
		"body",
	}, "\n")
	note := vault.ParseNote("types.md", []byte(src))
	require.NotNil(t, note.FM)
	require.NoError(t, note.FM.ParseErr)

	require.Equal(t, "2026-07-15", note.FM.Fields["date"])
	require.IsType(t, int64(0), note.FM.Fields["meeting_count"])
	require.Equal(t, int64(3), note.FM.Fields["meeting_count"])
	require.Nil(t, note.FM.Fields["empty"])
	require.Nil(t, note.FM.Fields["nothing"])
	numbers, ok := note.FM.Fields["numbers"].([]any)
	require.True(t, ok)
	require.Equal(t, []any{int64(1), int64(2)}, numbers)
	mapping, ok := note.FM.Fields["mapping"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, int64(4), mapping["child"])

	vals, wasString, ok := note.FM.StringList("bare")
	require.True(t, ok)
	require.True(t, wasString)
	require.Equal(t, []string{"single value"}, vals)

	vals, wasString, ok = note.FM.StringList("list")
	require.True(t, ok)
	require.False(t, wasString)
	require.Equal(t, []string{"first", "second"}, vals)

	vals, wasString, ok = note.FM.StringList("empty_list")
	require.True(t, ok)
	require.False(t, wasString)
	require.Empty(t, vals)

	for _, key := range []string{"missing", "meeting_count", "mixed", "numbers", "mapping", "nothing"} {
		_, _, ok = note.FM.StringList(key)
		require.False(t, ok, "key %q", key)
	}
}

func TestParseNoteFrontmatterTopLevelKeyLinesAreFileLines(t *testing.T) {
	t.Parallel()

	note := vault.ParseNote("lines.md", []byte("---\nfirst: one\nnested:\n  child: two\nlast: three\n---\nbody\n"))
	require.NotNil(t, note.FM)
	require.Equal(t, 2, note.FM.Lines["first"])
	require.Equal(t, 3, note.FM.Lines["nested"])
	require.Equal(t, 5, note.FM.Lines["last"])
	_, childIsTopLevel := note.FM.Lines["child"]
	require.False(t, childIsTopLevel)
}

func TestParseNoteExtractsWikilinkForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		src      string
		target   string
		alias    string
		fragment string
		embed    bool
	}{
		{name: "bare", src: "[[Target]]", target: "Target"},
		{name: "alias", src: "[[Target|Alias]]", target: "Target", alias: "Alias"},
		{name: "fragment", src: "[[Target#Heading]]", target: "Target", fragment: "Heading"},
		{name: "embed", src: "![[file.png]]", target: "file.png", embed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			note := vault.ParseNote("links.md", []byte(tt.src+"\n"))
			require.Len(t, note.Links, 1)
			link := note.Links[0]
			require.Equal(t, vault.KindWikilink, link.Kind)
			require.Equal(t, tt.target, link.Target)
			require.Equal(t, tt.alias, link.Alias)
			require.Equal(t, tt.fragment, link.Fragment)
			require.Equal(t, tt.embed, link.Embed)
			require.False(t, link.InFrontmatter)
			require.Equal(t, 1, link.Line)
		})
	}
}

func TestParseNoteIgnoresWikilinksInCode(t *testing.T) {
	t.Parallel()

	src := strings.Join([]string{
		"[[VisibleBefore]]",
		"```go",
		"[[InBacktickFence]]",
		"```",
		"~~~text",
		"[[InTildeFence]]",
		"~~~",
		"Text `[[InInlineCode]]` text [[VisibleAfter]].",
	}, "\n")
	note := vault.ParseNote("code.md", []byte(src))
	require.Len(t, note.Links, 2)
	require.Equal(t, "VisibleBefore", note.Links[0].Target)
	require.Equal(t, 1, note.Links[0].Line)
	require.Equal(t, "VisibleAfter", note.Links[1].Target)
	require.Equal(t, 8, note.Links[1].Line)
}

func TestParseNoteExtractsFrontmatterWikilinksAndUsesFileLineNumbers(t *testing.T) {
	t.Parallel()

	src := "---\nrelated: \"See [[Frontmatter Target]]\"\n---\nintro\n[[Body Target]]\n"
	note := vault.ParseNote("frontmatter-links.md", []byte(src))
	require.Len(t, note.Links, 2)

	byTarget := make(map[string]vault.Link, len(note.Links))
	for _, link := range note.Links {
		byTarget[link.Target] = link
	}
	fmLink, ok := byTarget["Frontmatter Target"]
	require.True(t, ok)
	require.True(t, fmLink.InFrontmatter)
	require.Equal(t, vault.KindWikilink, fmLink.Kind)

	bodyLink, ok := byTarget["Body Target"]
	require.True(t, ok)
	require.False(t, bodyLink.InFrontmatter)
	require.Equal(t, 5, bodyLink.Line)
}

func TestParseNoteExtractsLocalMarkdownLinksOnly(t *testing.T) {
	t.Parallel()

	src := strings.Join([]string{
		"[local](Other%20Note.md)",
		"[web](https://example.com/page)",
		"[mail](mailto:person@example.com)",
		"[same page](#heading)",
	}, "\n")
	note := vault.ParseNote("markdown.md", []byte(src))
	require.Len(t, note.Links, 1)
	require.Equal(t, vault.KindMarkdown, note.Links[0].Kind)
	require.Equal(t, "Other Note.md", note.Links[0].Target)
	require.False(t, note.Links[0].Embed)
}

func TestResolveBareTargetsByCaseInsensitiveNFCBasename(t *testing.T) {
	t.Parallel()

	nfdPath := "Unicode/Cafe\u0301.md"
	ix, _ := buildVault(t, map[string]string{
		"Notes/Mixed Case.md": "body\n",
		nfdPath:               "body\n",
	})

	require.Equal(t, []string{"Notes/Mixed Case.md"}, ix.Resolve("Source.md", vault.Link{Target: "mixed case", Kind: vault.KindWikilink}))
	require.Equal(t, []string{nfdPath}, ix.Resolve("Source.md", vault.Link{Target: "Caf\u00e9", Kind: vault.KindWikilink}))
	require.Empty(t, ix.Resolve("Source.md", vault.Link{Target: "Does Not Exist", Kind: vault.KindWikilink}))
}

func TestResolveUsesAliasesOnlyWhenNoBasenameMatches(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"AliasList.md":    "---\naliases:\n  - Secret Name\n---\n",
		"AliasListTwo.md": "---\naliases:\n  - SECRET NAME\n---\n",
		"AliasString.md":  "---\naliases: Lone Alias\n---\n",
		"Preferred.md":    "body\n",
		"AlsoAlias.md":    "---\naliases: preferred\n---\n",
	})

	require.ElementsMatch(t, []string{"AliasList.md", "AliasListTwo.md"}, ix.Resolve("Source.md", vault.Link{Target: "secret name", Kind: vault.KindWikilink}))
	require.Equal(t, []string{"AliasString.md"}, ix.Resolve("Source.md", vault.Link{Target: "LONE ALIAS", Kind: vault.KindWikilink}))
	require.Equal(t, []string{"Preferred.md"}, ix.Resolve("Source.md", vault.Link{Target: "PREFERRED", Kind: vault.KindWikilink}))
}

func TestResolveSlashTargetAsVaultRelativePathWithoutBasenameFallback(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"Folder/Exact.md":    "body\n",
		"Somewhere/Other.md": "body\n",
	})

	require.Equal(t, []string{"Folder/Exact.md"}, ix.Resolve("Source.md", vault.Link{Target: "Folder/Exact", Kind: vault.KindWikilink}))
	require.Equal(t, []string{"Folder/Exact.md"}, ix.Resolve("Source.md", vault.Link{Target: "Folder/Exact.md", Kind: vault.KindWikilink}))
	require.Empty(t, ix.Resolve("Source.md", vault.Link{Target: "Missing/Exact", Kind: vault.KindWikilink}))
	require.Empty(t, ix.Resolve("Source.md", vault.Link{Target: "Missing/Other", Kind: vault.KindWikilink}))
}

func TestResolveBareAmbiguousBasenameReturnsEveryMatch(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"One/Duplicate.md": "body\n",
		"Two/duplicate.md": "body\n",
	})

	got := ix.Resolve("Source.md", vault.Link{Target: "DUPLICATE", Kind: vault.KindWikilink})
	require.ElementsMatch(t, []string{"One/Duplicate.md", "Two/duplicate.md"}, got)
}

func TestResolveTargetsWithExtensionsToNonMarkdownFilesByBasename(t *testing.T) {
	t.Parallel()

	ix, prof := buildVault(t, map[string]string{
		"+/diagram.png":    "png bytes",
		"Assets/chart.svg": "svg bytes",
	})
	require.Equal(t, "+", prof.Attachments)
	require.True(t, ix.Files["+/diagram.png"])
	require.True(t, ix.Files["Assets/chart.svg"])

	require.Equal(t, []string{"+/diagram.png"}, ix.Resolve("Source.md", vault.Link{Target: "diagram.png", Embed: true, Kind: vault.KindWikilink}))
	require.Equal(t, []string{"+/diagram.png"}, ix.Resolve("Source.md", vault.Link{Target: "diagram.png", Kind: vault.KindWikilink}))
	require.Equal(t, []string{"Assets/chart.svg"}, ix.Resolve("Source.md", vault.Link{Target: "chart.svg", Kind: vault.KindWikilink}))
}

func TestResolveMarkdownTargetsRelativeThenVaultRoot(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"shared.md":          "root\n",
		"root-only.md":       "root\n",
		"Docs/shared.md":     "relative\n",
		"Docs/Source.md":     "source\n",
		"Other/Source.md":    "source\n",
		"Other/unrelated.md": "other\n",
	})

	require.Equal(t, []string{"Docs/shared.md"}, ix.Resolve("Docs/Source.md", vault.Link{Target: "shared.md", Kind: vault.KindMarkdown}))
	require.Equal(t, []string{"root-only.md"}, ix.Resolve("Docs/Source.md", vault.Link{Target: "root-only.md", Kind: vault.KindMarkdown}))
}

func TestResolveEmptyFragmentTargetToSourceNote(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"Folder/Source.md": "# Heading\n",
	})

	got := ix.Resolve("Folder/Source.md", vault.Link{Target: "", Fragment: "Heading", Kind: vault.KindWikilink})
	require.Equal(t, []string{"Folder/Source.md"}, got)
}

func TestBuildIndexSkipsDotDirectoriesButIndexesRootDotFiles(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"Visible.md":              "body\n",
		".root-note.md":           "# not a note\n",
		".root-data":              "data\n",
		".obsidian/Hidden.md":     "body\n",
		".git/Also Hidden.md":     "body\n",
		"Folder/.private/Deep.md": "body\n",
	})

	require.True(t, ix.Files[".root-note.md"])
	require.True(t, ix.Files[".root-data"])
	require.NotContains(t, ix.Notes, ".root-note.md")

	for _, rel := range []string{".obsidian/Hidden.md", ".git/Also Hidden.md", "Folder/.private/Deep.md"} {
		require.False(t, ix.Files[rel], rel)
		require.NotContains(t, ix.Notes, rel)
	}
	for dir := range ix.Dirs {
		require.False(t, strings.HasPrefix(dir, ".obsidian"), dir)
		require.False(t, strings.HasPrefix(dir, ".git"), dir)
		require.NotContains(t, dir, "/.private")
	}
	require.Empty(t, ix.Resolve("Visible.md", vault.Link{Target: "Hidden", Kind: vault.KindWikilink}))
	require.Empty(t, ix.Resolve("Visible.md", vault.Link{Target: "Also Hidden", Kind: vault.KindWikilink}))
}

func TestBuildIndexPopulatesBacklinksUnderEveryResolvedTarget(t *testing.T) {
	t.Parallel()

	ix, _ := buildVault(t, map[string]string{
		"Source.md":    "A link to [[X]].\n",
		"Targets/X.md": "# X\n",
	})

	refs := ix.Backlinks["Targets/X.md"]
	require.Len(t, refs, 1)
	require.Equal(t, "Source.md", refs[0].Source)
	require.Equal(t, "X", refs[0].Link.Target)
	require.Equal(t, vault.KindWikilink, refs[0].Link.Kind)
}
