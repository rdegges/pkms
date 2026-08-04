package ingest

import (
	"bytes"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bareURLRe matches an http(s) URL run. It is deliberately greedy up to the
// first whitespace or angle bracket; trailing sentence punctuation is
// trimmed afterward by trimTrailingURLPunct (the GFM-autolink problem).
var bareURLRe = regexp.MustCompile(`https?://[^\s<>` + "`" + `"']+`)

// skipLinkify names elements whose text must NOT be linkified: already a
// link, or code/verbatim/non-content contexts.
var skipLinkify = map[atom.Atom]bool{
	atom.A:      true,
	atom.Code:   true,
	atom.Pre:    true,
	atom.Script: true,
	atom.Style:  true,
	atom.Kbd:    true,
	atom.Samp:   true,
}

// linkifyBareURLs wraps bare http(s) URLs that appear as TEXT into <a> tags,
// so the markdown converter emits them as link destinations (which it never
// escapes) instead of escaping their `_`/`*` as prose (which breaks the
// link). URLs already inside <a>, and text inside code/pre/script/style,
// are left untouched. On any parse/render error the input is returned
// unchanged — linkifying is a best-effort enhancement, never a hard failure.
func linkifyBareURLs(fragment string) string {
	if !strings.Contains(fragment, "http") {
		return fragment // fast path: nothing to do
	}
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return fragment
	}
	body := findBody(doc)
	if body == nil {
		return fragment
	}
	walkLinkify(body, false)

	var buf bytes.Buffer
	for c := body.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return fragment
		}
	}
	return buf.String()
}

func findBody(n *html.Node) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == atom.Body {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if b := findBody(c); b != nil {
			return b
		}
	}
	return nil
}

// walkLinkify recurses the tree; inSkip is true once we are inside any
// skipLinkify ancestor. Text nodes outside skip context get their bare URLs
// replaced with a mix of text + <a> nodes.
func walkLinkify(n *html.Node, inSkip bool) {
	// Snapshot children first: we mutate the sibling list while iterating.
	var children []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		children = append(children, c)
	}
	for _, c := range children {
		switch c.Type {
		case html.ElementNode:
			walkLinkify(c, inSkip || skipLinkify[c.DataAtom])
		case html.TextNode:
			if !inSkip {
				linkifyTextNode(c)
			}
		}
	}
}

// linkifyTextNode replaces the text node with a sequence of text and <a>
// nodes when its content holds bare URLs.
func linkifyTextNode(n *html.Node) {
	text := n.Data
	locs := bareURLRe.FindAllStringIndex(text, -1)
	if locs == nil {
		return
	}

	parent := n.Parent
	var pieces []*html.Node
	last := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		raw := text[start:end]
		url, trailing := trimTrailingURLPunct(raw)
		if url == "" {
			continue // whole match was punctuation somehow; leave as text
		}
		if start > last {
			pieces = append(pieces, textNode(text[last:start]))
		}
		pieces = append(pieces, anchorNode(url))
		if trailing != "" {
			pieces = append(pieces, textNode(trailing))
		}
		last = end
	}
	if len(pieces) == 0 {
		return
	}
	if last < len(text) {
		pieces = append(pieces, textNode(text[last:]))
	}

	for _, p := range pieces {
		parent.InsertBefore(p, n)
	}
	parent.RemoveChild(n)
}

func textNode(s string) *html.Node {
	return &html.Node{Type: html.TextNode, Data: s}
}

func anchorNode(url string) *html.Node {
	a := &html.Node{Type: html.ElementNode, Data: "a", DataAtom: atom.A,
		Attr: []html.Attribute{{Key: "href", Val: url}}}
	a.AppendChild(textNode(url))
	return a
}

// trimTrailingURLPunct splits a raw URL match into the URL proper and any
// trailing sentence punctuation that shouldn't be part of the link (GFM
// autolink semantics, simplified): trailing runs of .,;:!?'" are never part
// of a URL, and a trailing ) or ] is only part of the URL if balanced.
func trimTrailingURLPunct(raw string) (url, trailing string) {
	i := len(raw)
	for i > 0 {
		ch := raw[i-1]
		switch {
		case strings.IndexByte(".,;:!?'\"", ch) >= 0:
			i--
		case ch == ')':
			if strings.Count(raw[:i], "(") <= strings.Count(raw[:i], ")")-1 {
				i-- // more ) than ( → this ) is trailing
			} else {
				goto done
			}
		case ch == ']':
			if strings.Count(raw[:i], "[") <= strings.Count(raw[:i], "]")-1 {
				i--
			} else {
				goto done
			}
		default:
			goto done
		}
	}
done:
	return raw[:i], raw[i:]
}
