package vault

import (
	"bytes"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"go.abhg.dev/goldmark/wikilink"
)

// MaxBodyParseSize caps goldmark parsing (SPEC §14); bigger bodies skip
// AST-derived analysis and lint reports a file-too-large finding.
const MaxBodyParseSize = 10 << 20

// LinkKind distinguishes link syntaxes.
type LinkKind string

const (
	KindWikilink LinkKind = "wikilink"
	KindMarkdown LinkKind = "markdown"
)

// Link is one outgoing reference from a note.
type Link struct {
	Target        string // raw target as written, fragment stripped
	Fragment      string // after '#' (heading or ^blockid), no '#'
	Alias         string // display text for [[t|alias]]; "" when none
	Embed         bool
	Kind          LinkKind
	Line          int // 1-based; 0 = unknown
	InFrontmatter bool
}

// Note is one parsed vault markdown file.
type Note struct {
	// RelPath is the vault-relative path as it exists on disk.
	RelPath string
	Src     []byte
	FM      *Frontmatter // nil when the file has no frontmatter block
	Body    []byte
	// BodyOffset is the byte offset of Body within Src.
	BodyOffset int
	// TooLarge marks bodies over MaxBodyParseSize (no AST analysis done).
	TooLarge bool

	Links    []Link
	Headings []string // heading text, document order
	BlockIDs map[string]bool

	lineIndex []int // byte offsets of line starts in Src
}

var mdParser = goldmark.New(goldmark.WithExtensions(&wikilink.Extender{}))

// ParseNote parses one note from raw bytes.
func ParseNote(relPath string, src []byte) *Note {
	n := &Note{RelPath: relPath, Src: src, BlockIDs: map[string]bool{}}
	n.FM, n.Body, n.BodyOffset = splitFrontmatter(src)
	n.buildLineIndex()

	if n.FM != nil {
		n.extractFrontmatterLinks()
	}
	if len(n.Body) > MaxBodyParseSize {
		n.TooLarge = true
		return n
	}
	n.extractBodyLinks()
	n.extractBlockIDs()
	return n
}

func (n *Note) buildLineIndex() {
	n.lineIndex = []int{0}
	for i, b := range n.Src {
		if b == '\n' {
			n.lineIndex = append(n.lineIndex, i+1)
		}
	}
}

// LineOf converts a byte offset in Src to a 1-based line number.
func (n *Note) LineOf(offset int) int {
	i := sort.Search(len(n.lineIndex), func(i int) bool { return n.lineIndex[i] > offset })
	return i
}

// LineCount returns the number of lines in the note.
func (n *Note) LineCount() int {
	c := len(n.lineIndex)
	if len(n.Src) > 0 && n.Src[len(n.Src)-1] == '\n' {
		c--
	}
	return c
}

// wikilinkRe matches [[target]], [[target|alias]], [[target#frag]],
// ![[embed]] inside plain strings (frontmatter values).
var wikilinkRe = regexp.MustCompile(`(!?)\[\[([^\][|#]*)(?:#([^\][|]*))?(?:\|([^\][]*))?\]\]`)

// ParseWikilinkString parses a single frontmatter-style wikilink string like
// "[[Jane Doe]]". ok is false unless the whole string is exactly one link.
func ParseWikilinkString(s string) (target, fragment, alias string, embed, ok bool) {
	m := wikilinkRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[0] != strings.TrimSpace(s) {
		return "", "", "", false, false
	}
	return m[2], m[3], m[4], m[1] == "!", true
}

func (n *Note) extractFrontmatterLinks() {
	var walk func(v any, line int)
	walk = func(v any, line int) {
		switch t := v.(type) {
		case string:
			for _, m := range wikilinkRe.FindAllStringSubmatch(t, -1) {
				n.Links = append(n.Links, Link{
					Target: m[2], Fragment: m[3], Alias: m[4],
					Embed: m[1] == "!", Kind: KindWikilink,
					Line: line, InFrontmatter: true,
				})
			}
		case []any:
			for _, e := range t {
				walk(e, line)
			}
		case map[string]any:
			for _, e := range t {
				walk(e, line)
			}
		}
	}
	for key, v := range n.FM.Fields {
		walk(v, n.FM.Lines[key])
	}
}

func (n *Note) extractBodyLinks() {
	doc := mdParser.Parser().Parse(text.NewReader(n.Body))
	_ = gast.Walk(doc, func(node gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch t := node.(type) {
		case *wikilink.Node:
			n.addWikilinkNode(t)
		case *gast.Link:
			n.addMarkdownLink(string(t.Destination), false, t)
		case *gast.Image:
			n.addMarkdownLink(string(t.Destination), true, t)
		case *gast.Heading:
			n.Headings = append(n.Headings, string(headingText(t, n.Body)))
		}
		return gast.WalkContinue, nil
	})
}

// headingText concatenates the text content of a heading node.
func headingText(node gast.Node, src []byte) []byte {
	var buf bytes.Buffer
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*gast.Text); ok {
			buf.Write(t.Segment.Value(src))
		} else {
			buf.Write(headingText(c, src))
		}
	}
	return buf.Bytes()
}

func (n *Note) addWikilinkNode(w *wikilink.Node) {
	target := string(w.Target)
	fragment := strings.TrimPrefix(string(w.Fragment), "#")
	// Alias and position live in the child text nodes (the wikilink node
	// itself carries no segment — deps report §2).
	alias := ""
	line := 0
	if first, ok := w.FirstChild().(*gast.Text); ok {
		display := string(first.Segment.Value(n.Body))
		rawTarget := target
		if fragment != "" {
			rawTarget = target + "#" + fragment
		}
		if display != rawTarget && display != "" {
			alias = display
		}
		line = n.LineOf(n.BodyOffset + first.Segment.Start)
	}
	n.Links = append(n.Links, Link{
		Target: target, Fragment: fragment, Alias: alias,
		Embed: w.Embed, Kind: KindWikilink, Line: line,
	})
}

func (n *Note) addMarkdownLink(dest string, embed bool, node gast.Node) {
	// Only vault-internal destinations participate (SPEC §5.7).
	if dest == "" || strings.Contains(dest, "://") ||
		strings.HasPrefix(dest, "mailto:") || strings.HasPrefix(dest, "#") {
		return
	}
	if decoded, err := url.PathUnescape(dest); err == nil {
		dest = decoded
	}
	target, fragment, _ := strings.Cut(dest, "#")
	if target == "" {
		return
	}
	line := 0
	if t, ok := node.FirstChild().(*gast.Text); ok {
		line = n.LineOf(n.BodyOffset + t.Segment.Start)
	}
	n.Links = append(n.Links, Link{
		Target: target, Fragment: fragment,
		Embed: embed, Kind: KindMarkdown, Line: line,
	})
}

// blockIDRe: Obsidian block anchors are ^id at end of a line.
var blockIDRe = regexp.MustCompile(`(?m)(?:^|\s)\^([A-Za-z0-9-]+)\s*$`)

func (n *Note) extractBlockIDs() {
	for _, m := range blockIDRe.FindAllSubmatch(n.Body, -1) {
		n.BlockIDs[string(m[1])] = true
	}
}

// HasHeading reports whether the note contains the given heading,
// case-insensitively (Obsidian anchor matching).
func (n *Note) HasHeading(h string) bool {
	for _, have := range n.Headings {
		if strings.EqualFold(strings.TrimSpace(have), strings.TrimSpace(h)) {
			return true
		}
	}
	return false
}

// Basename returns the file name without the .md extension.
func (n *Note) Basename() string {
	base := n.RelPath
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".md")
}

// Aliases returns the frontmatter aliases list (string or list form).
func (n *Note) Aliases() []string {
	if n.FM == nil {
		return nil
	}
	vals, _, ok := n.FM.StringList("aliases")
	if !ok {
		return nil
	}
	return vals
}
