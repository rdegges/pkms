package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLinkifyBareURLs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// wantHref, if set, must appear as an href in the output.
		wantHref string
		// wantNoNewAnchor: the output must contain no MORE <a than the input.
		wantNoNewAnchor bool
		// wantUnchanged: output must equal input (fast path / nothing to do).
		wantUnchanged bool
	}{
		{name: "bare url in paragraph",
			in: `<p>see https://x.com/a_b?c=d here</p>`, wantHref: "https://x.com/a_b?c=d"},
		{name: "url already in anchor is not re-wrapped",
			in: `<p><a href="https://y.com/c_d">label</a></p>`, wantNoNewAnchor: true},
		{name: "url text inside anchor is not linkified",
			in: `<p><a href="https://y.com/x">https://y.com/z_z</a></p>`, wantNoNewAnchor: true},
		{name: "url in code is left alone",
			in: `<p><code>https://x.com/a_b</code></p>`, wantNoNewAnchor: true},
		{name: "url in pre is left alone",
			in: `<pre>https://x.com/a_b</pre>`, wantNoNewAnchor: true},
		{name: "no url is unchanged (fast path)",
			in: `<p>just some text, no links</p>`, wantUnchanged: true},
		{name: "trailing period not part of href",
			in: `<p>go to https://x.com/a_b.</p>`, wantHref: "https://x.com/a_b"},
		{name: "trailing paren not part of href",
			in: `<p>(see https://x.com/a_b)</p>`, wantHref: "https://x.com/a_b"},
		{name: "balanced parens kept in href",
			in: `<p>https://en.wikipedia.org/wiki/Foo_(bar)</p>`, wantHref: "https://en.wikipedia.org/wiki/Foo_(bar)"},
		{name: "ftp and javascript schemes are not linkified",
			in: `<p>ftp://x.com/a and javascript:alert(1) stay text</p>`, wantNoNewAnchor: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := linkifyBareURLs(tc.in)
			switch {
			case tc.wantUnchanged:
				require.Equal(t, tc.in, out)
			case tc.wantNoNewAnchor:
				require.Equal(t, strings.Count(tc.in, "<a "), strings.Count(out, "<a "),
					"no new <a> introduced: %s", out)
			case tc.wantHref != "":
				require.Contains(t, out, `href="`+tc.wantHref+`"`, "output: %s", out)
			}
		})
	}
}

func TestLinkifyMultipleURLsInOneTextNode(t *testing.T) {
	out := linkifyBareURLs(`<p>a https://x.com/1_a and b https://y.com/2_b end</p>`)
	require.Equal(t, 2, strings.Count(out, "<a "), out)
	require.Contains(t, out, `href="https://x.com/1_a"`)
	require.Contains(t, out, `href="https://y.com/2_b"`)
}

func TestLinkifyNestedSkipContext(t *testing.T) {
	// A bare URL inside <pre><code> (doubly-nested skip) stays untouched;
	// one in a sibling <p> gets linkified.
	out := linkifyBareURLs(`<div><pre><code>https://x.com/a_b</code></pre><p>https://y.com/c_d</p></div>`)
	require.Equal(t, 1, strings.Count(out, "<a "), out)
	require.Contains(t, out, `href="https://y.com/c_d"`)
	require.NotContains(t, out, `href="https://x.com/a_b"`)
}

func TestTrimTrailingURLPunct(t *testing.T) {
	cases := map[string][2]string{ // input -> [url, trailing]
		"https://x.com/a":       {"https://x.com/a", ""},
		"https://x.com/a.":      {"https://x.com/a", "."},
		"https://x.com/a),":     {"https://x.com/a", "),"},
		"https://x.com/a!?":     {"https://x.com/a", "!?"},
		"https://x.com/Foo_(b)": {"https://x.com/Foo_(b)", ""}, // balanced
		"https://x.com/a]":      {"https://x.com/a", "]"},      // unbalanced ]
	}
	for in, want := range cases {
		url, trailing := trimTrailingURLPunct(in)
		require.Equal(t, want[0], url, "url for %q", in)
		require.Equal(t, want[1], trailing, "trailing for %q", in)
	}
}

func TestLinkifyMalformedHTMLReturnsBestEffort(t *testing.T) {
	// Broken/unbalanced HTML must never panic or drop content wholesale.
	for _, in := range []string{
		`<p>unclosed https://x.com/a_b`,
		`<<>&nbsp; https://x.com/a_b <p>`,
		`plain https://x.com/a_b no tags`,
	} {
		out := linkifyBareURLs(in)
		require.Contains(t, out, "x.com/a_b", "content preserved for %q", in)
	}
}

func TestLinkifyStopsAtUnicodeWhitespace(t *testing.T) {
	// Go's \s is ASCII-only; nbsp (U+00A0) and zero-width space (U+200B)
	// right after a URL must terminate it, not get absorbed (red-team HIGH).
	for _, sp := range []string{" ", "\u00a0", "\u200b"} { // space, nbsp, ZWSP
		out := linkifyBareURLs("<p>see https://x.com/a_b" + sp + "next word</p>")
		require.Contains(t, out, `href="https://x.com/a_b"`, "space %U must end the URL", []rune(sp)[0])
		require.NotContains(t, out, "next\"", "the following word is not pulled into the href")
	}
	// Two URLs separated only by nbsp both linkify (the match must split).
	out := linkifyBareURLs("<p>https://x.com/a_b\u00a0https://y.com/c_d</p>")
	require.Equal(t, 2, strings.Count(out, "<a "), out)
}

func TestLinkifyHostlessSchemeStaysText(t *testing.T) {
	out := linkifyBareURLs(`<p>Go to https://. Done</p>`)
	require.NotContains(t, out, "<a ", "a hostless scheme is not a link: %s", out)
	require.Contains(t, out, "https://", "text preserved")
}

func TestLinkifyUppercaseScheme(t *testing.T) {
	out := linkifyBareURLs(`<p>HTTPS://X.COM/A_B here</p>`)
	require.Contains(t, out, `href="HTTPS://X.COM/A_B"`, out)
}

func TestLinkifyApostropheInPath(t *testing.T) {
	// Mid-URL apostrophe stays in the URL (GFM), not truncated at "it".
	// (linkify emits it entity-encoded as &#39;, which round-trips.)
	out := linkifyBareURLs(`<p>https://x.com/it's_here ok</p>`)
	require.Contains(t, out, `it&#39;s_here`, out)
	// End-to-end: the markdown link destination has the apostrophe restored.
	md, err := ConvertHTML(`<p>https://x.com/it's_here ok</p>`)
	require.NoError(t, err)
	require.Contains(t, md, "it's_here)", "apostrophe preserved in destination: %s", md)
}
