package vault

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func findLink(t *testing.T, links []Link, target string) Link {
	t.Helper()
	for _, l := range links {
		if l.Target == target {
			return l
		}
	}
	t.Fatalf("link %q not found in %+v", target, links)
	return Link{}
}

func TestBodyWikilinks(t *testing.T) {
	src := []byte(`# Doc

Plain [[Target Note]] here.
Aliased [[Target Note|The Alias]] here.
Fragment [[Other#Some Heading]] and block [[Other#^abc123]].
Embed ![[image.png]].
Path [[Sub/Dir/Note]].

` + "```\n[[Inside Code Fence]]\n```\n" + "And `[[inline code]]` too.\n")
	n := ParseNote("a.md", src)

	plain := findLink(t, n.Links, "Target Note")
	require.Equal(t, KindWikilink, plain.Kind)
	require.Equal(t, 3, plain.Line)
	require.False(t, plain.Embed)

	var aliased *Link
	for i := range n.Links {
		if n.Links[i].Alias == "The Alias" {
			aliased = &n.Links[i]
		}
	}
	require.NotNil(t, aliased, "alias comes from child text nodes")
	require.Equal(t, "Target Note", aliased.Target)
	require.Equal(t, 4, aliased.Line)

	frag := findLink(t, n.Links, "Other")
	require.Contains(t, []string{"Some Heading", "^abc123"}, frag.Fragment)

	embed := findLink(t, n.Links, "image.png")
	require.True(t, embed.Embed)

	pathed := findLink(t, n.Links, "Sub/Dir/Note")
	require.Equal(t, 7, pathed.Line)

	for _, l := range n.Links {
		require.NotEqual(t, "Inside Code Fence", l.Target, "code fences are excluded")
		require.NotEqual(t, "inline code", l.Target, "inline code is excluded")
	}
}

func TestMarkdownLinks(t *testing.T) {
	src := []byte("[text](Other%20Note.md) [ext](https://example.com) [anchor](#here)\n![img](assets/pic.png)\n")
	n := ParseNote("dir/a.md", src)

	md := findLink(t, n.Links, "Other Note.md")
	require.Equal(t, KindMarkdown, md.Kind)

	img := findLink(t, n.Links, "assets/pic.png")
	require.True(t, img.Embed)

	for _, l := range n.Links {
		require.NotContains(t, l.Target, "example.com", "external URLs excluded")
		require.NotEqual(t, "#here", l.Target, "same-page anchors excluded")
	}
}

func TestFrontmatterLinks(t *testing.T) {
	src := []byte(`---
attendees:
  - "[[Jane Doe]]"
  - "[[Sam Rivera]]"
related: "[[Some Project]]"
---
body
`)
	n := ParseNote("m.md", src)
	jd := findLink(t, n.Links, "Jane Doe")
	require.True(t, jd.InFrontmatter)
	require.Equal(t, 2, jd.Line, "list links carry the key's line")
	sp := findLink(t, n.Links, "Some Project")
	require.Equal(t, 5, sp.Line)
}

func TestParseWikilinkString(t *testing.T) {
	target, frag, alias, embed, ok := ParseWikilinkString("[[Jane Doe]]")
	require.True(t, ok)
	require.Equal(t, "Jane Doe", target)
	require.Empty(t, frag)
	require.Empty(t, alias)
	require.False(t, embed)

	_, _, alias, _, ok = ParseWikilinkString("[[Jane Doe|Jane]]")
	require.True(t, ok)
	require.Equal(t, "Jane", alias)

	_, _, _, _, ok = ParseWikilinkString("Jane Doe")
	require.False(t, ok, "bare string is not a wikilink")

	_, _, _, _, ok = ParseWikilinkString("x [[Jane Doe]] y")
	require.False(t, ok, "must be exactly one link")
}

func TestHeadingsAndBlockIDs(t *testing.T) {
	src := []byte("# Top\n\n## Sub *Heading*\n\ntext ^block-1\n")
	n := ParseNote("a.md", src)
	require.Equal(t, []string{"Top", "Sub Heading"}, n.Headings)
	require.True(t, n.HasHeading("sub heading"), "heading match is case-insensitive")
	require.True(t, n.BlockIDs["block-1"])
}

func TestLineHelpers(t *testing.T) {
	n := ParseNote("a.md", []byte("one\ntwo\nthree\n"))
	require.Equal(t, 1, n.LineOf(0))
	require.Equal(t, 2, n.LineOf(4))
	require.Equal(t, 3, n.LineCount())
	require.Equal(t, "a", ParseNote("x/y/a.md", []byte("hi")).Basename())
}
