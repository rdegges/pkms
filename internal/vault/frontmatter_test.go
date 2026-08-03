package vault

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitNoFrontmatter(t *testing.T) {
	src := []byte("# Title\nbody\n")
	fm, body, off := splitFrontmatter(src)
	require.Nil(t, fm)
	require.Equal(t, src, body)
	require.Zero(t, off)
}

func TestSplitRequiresFenceAtByteZero(t *testing.T) {
	fm, _, _ := splitFrontmatter([]byte("\n---\ndate: 2026-01-01\n---\n"))
	require.Nil(t, fm, "Obsidian only recognizes frontmatter at byte 0")
}

func TestSplitValid(t *testing.T) {
	src := []byte("---\ndate: 2026-07-15\ntags: [a, b]\ncount: 3\nnully: null\n---\n# Body\n")
	fm, body, off := splitFrontmatter(src)
	require.NotNil(t, fm)
	require.NoError(t, fm.ParseErr)
	require.False(t, fm.Unclosed)
	require.Equal(t, "# Body\n", string(body))
	require.Equal(t, []byte(src[off:]), body)
	require.Equal(t, 6, fm.EndLine)

	require.Equal(t, "2026-07-15", fm.Fields["date"], "ISO dates stay strings")
	require.Equal(t, int64(3), fm.Fields["count"], "ints normalize to int64")
	require.Nil(t, fm.Fields["nully"])
	require.Equal(t, []any{"a", "b"}, fm.Fields["tags"])
	require.Equal(t, []string{"date", "tags", "count", "nully"}, fm.Order)

	require.Equal(t, 2, fm.Lines["date"], "line numbers are file-relative")
	require.Equal(t, 4, fm.Lines["count"])
	require.Equal(t, "2026-07-15", fm.RawScalars["date"])
}

func TestSplitUnclosed(t *testing.T) {
	fm, body, _ := splitFrontmatter([]byte("---\ndate: 2026-01-01\n# oops no closing fence"))
	require.NotNil(t, fm)
	require.True(t, fm.Unclosed)
	require.Nil(t, body)
}

func TestSplitDotsFence(t *testing.T) {
	fm, body, _ := splitFrontmatter([]byte("---\nk: v\n...\nbody\n"))
	require.NotNil(t, fm)
	require.False(t, fm.Unclosed)
	require.Equal(t, "body\n", string(body))
}

func TestParseErrorSurfaced(t *testing.T) {
	fm, _, _ := splitFrontmatter([]byte("---\nk: \"unterminated\n---\nbody\n"))
	require.NotNil(t, fm)
	require.Error(t, fm.ParseErr)
	require.Nil(t, fm.Fields)
}

func TestRawScalarPreservesSpelling(t *testing.T) {
	fm, _, _ := splitFrontmatter([]byte("---\nlast_met: 2026/07/15\n---\n"))
	require.NoError(t, fm.ParseErr)
	require.Equal(t, "2026/07/15", fm.RawScalars["last_met"])
}

func TestStringListNormalization(t *testing.T) {
	fm, _, _ := splitFrontmatter([]byte("---\naliases: solo\ntags:\n  - a\n  - b\nbad: 3\n---\n"))
	require.NoError(t, fm.ParseErr)

	vals, wasString, ok := fm.StringList("aliases")
	require.True(t, ok)
	require.True(t, wasString, "string form is detected (lint flags it)")
	require.Equal(t, []string{"solo"}, vals)

	vals, wasString, ok = fm.StringList("tags")
	require.True(t, ok)
	require.False(t, wasString)
	require.Equal(t, []string{"a", "b"}, vals)

	_, _, ok = fm.StringList("bad")
	require.False(t, ok)
	_, _, ok = fm.StringList("absent")
	require.False(t, ok)
}
