package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/unicode/norm"
)

// buildTestVault writes files (relpath -> content) into a temp dir.
func buildTestVault(t *testing.T, files map[string]string) *Index {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	ix, err := BuildIndex(root, WalkOptions{AttachmentsDir: "+"})
	require.NoError(t, err)
	return ix
}

func TestWalkSkipsDotDirsIndexesDotFiles(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"Note.md":            "hi",
		".obsidian/app.json": "{}",
		".test_write":        "",
		"+/pic.png":          "binary",
		"+/stray.md":         "not a note",
	})
	require.Contains(t, ix.Notes, "Note.md")
	require.NotContains(t, ix.Files, ".obsidian/app.json", "dot-dirs skipped")
	require.Contains(t, ix.Files, ".test_write", "root dotfiles ARE indexed (junk rules)")
	require.Contains(t, ix.Files, "+/pic.png")
	require.NotContains(t, ix.Notes, "+/stray.md", "attachments dir never parsed as notes")
	require.Contains(t, ix.Files, "+/stray.md")
}

func TestResolveByBasenameCaseInsensitive(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"People/Jane Doe.md": "hello",
		"a.md":               "[[jane doe]]",
	})
	got := ix.Resolve("a.md", Link{Target: "jane doe", Kind: KindWikilink})
	require.Equal(t, []string{"People/Jane Doe.md"}, got)
}

func TestResolveNFC(t *testing.T) {
	// macOS writes NFD filenames; link text is usually NFC.
	nfdName := norm.NFD.String("Café.md")
	ix := buildTestVault(t, map[string]string{
		"Dir/" + nfdName: "x",
		"a.md":           "[[Café]]",
	})
	got := ix.Resolve("a.md", Link{Target: norm.NFC.String("Café"), Kind: KindWikilink})
	require.Len(t, got, 1)
}

func TestResolveByPathAndExtension(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"Sub/Dir/Note.md": "x",
		"a.md":            "y",
	})
	require.Len(t, ix.Resolve("a.md", Link{Target: "Sub/Dir/Note", Kind: KindWikilink}), 1)
	require.Len(t, ix.Resolve("a.md", Link{Target: "Sub/Dir/Note.md", Kind: KindWikilink}), 1)
	require.Empty(t, ix.Resolve("a.md", Link{Target: "Sub/Nope", Kind: KindWikilink}))
}

func TestResolveAmbiguousBasenames(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"X/Note.md": "1",
		"Y/Note.md": "2",
		"a.md":      "[[Note]]",
	})
	got := ix.Resolve("a.md", Link{Target: "Note", Kind: KindWikilink})
	require.Len(t, got, 2, "ambiguous but NOT broken (SPEC §5.4)")
	require.Len(t, ix.DuplicateBasenames(), 1)
}

func TestResolveAlias(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"Long Official Name.md": "---\naliases: [Shorty]\n---\nx",
		"a.md":                  "[[Shorty]]",
	})
	got := ix.Resolve("a.md", Link{Target: "Shorty", Kind: KindWikilink})
	require.Equal(t, []string{"Long Official Name.md"}, got)
}

func TestResolveEmbedNonMD(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"+/diagram.png": "png",
		"a.md":          "![[diagram.png]]",
	})
	got := ix.Resolve("a.md", Link{Target: "diagram.png", Embed: true, Kind: KindWikilink})
	require.Equal(t, []string{"+/diagram.png"}, got)
}

func TestResolveSelfHeading(t *testing.T) {
	ix := buildTestVault(t, map[string]string{"a.md": "# H\n[[#H]]"})
	got := ix.Resolve("a.md", Link{Target: "", Fragment: "H", Kind: KindWikilink})
	require.Equal(t, []string{"a.md"}, got)
}

func TestResolveMarkdownRelative(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"dir/Other.md": "x",
		"dir/a.md":     "[o](Other.md)",
		"Root.md":      "y",
	})
	require.Equal(t, []string{"dir/Other.md"},
		ix.Resolve("dir/a.md", Link{Target: "Other.md", Kind: KindMarkdown}))
	require.Equal(t, []string{"Root.md"},
		ix.Resolve("dir/a.md", Link{Target: "Root.md", Kind: KindMarkdown}),
		"falls back to vault root")
}

func TestBacklinks(t *testing.T) {
	ix := buildTestVault(t, map[string]string{
		"Target.md": "x",
		"From.md":   "[[Target]]",
	})
	refs := ix.Backlinks["Target.md"]
	require.Len(t, refs, 1)
	require.Equal(t, "From.md", refs[0].Source)
}
