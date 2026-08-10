package profile

import (
	"path"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// SanitizeAssetFilename is the shared neutralizer for attachment basenames
// (SPEC §31.2/§23): emitter-supplied names are hostile input.
func TestSanitizeAssetFilenameNeutralizesHostileNames(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "report.pdf", "report.pdf"},
		{"traversal", "../../etc/passwd", "..-..-etc-passwd"},
		{"absolute", "/etc/shadow", "-etc-shadow"},
		{"backslash traversal", `..\..\win.ini`, "..-..-win.ini"},
		{"wikilink syntax", "evil[[x]].png", "evil--x--.png"},
		{"pipe and hash", "a|b#c.png", "a-b-c.png"},
		{"colon (mac/win reserved)", "a:b.png", "a-b.png"},
		{"caret", "a^b.png", "a-b.png"},
		{"control chars dropped", "a\x00b\nc\x7f.png", "abc.png"},
		{"leading/trailing space trimmed", "  spaced.png", "spaced.png"},
		{"unicode kept", "café ☕.png", "café ☕.png"},
		{"no extension", "README", "README"},
		{"dotfile keeps its dot-name as the extension", ".bashrc", ".bashrc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeAssetFilename(tc.in)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, "/", "no path separator may survive")
			require.NotContains(t, got, `\`, "no backslash may survive")
			require.NotContains(t, got, "[[", "no wikilink syntax may survive")
		})
	}
}

// The whole point of the asset variant: truncation eats stem bytes, never the
// extension the OS and Obsidian dispatch on (SPEC §31.2).
func TestSanitizeAssetFilenamePreservesExtensionUnderTruncation(t *testing.T) {
	long := strings.Repeat("x", 400) + ".pdf"
	got := SanitizeAssetFilename(long)
	require.Equal(t, ".pdf", path.Ext(got), "extension survives")
	require.LessOrEqual(t, len(got), maxFilenameBytes, "basename stays under the cap")
	require.Greater(t, len(got), 100, "the stem is truncated, not discarded")
}

// Contrast with the note-filename sanitizer, which truncates blind: the two
// are deliberately different, and this pins that difference.
func TestSanitizeAssetFilenameDiffersFromNoteSanitizer(t *testing.T) {
	long := strings.Repeat("x", 400) + ".pdf"
	require.NotEqual(t, ".pdf", path.Ext(sanitizeFilename(long)),
		"the note sanitizer loses the extension — that is why the asset variant exists")
}

// An implausibly long "extension" is not an extension (SPEC §31.2 comment):
// a hostile name must not smuggle 200 bytes past the cap as a suffix.
func TestSanitizeAssetFilenameImplausibleExtension(t *testing.T) {
	in := "payload." + strings.Repeat("e", 200)
	got := SanitizeAssetFilename(in)
	require.LessOrEqual(t, len(got), maxFilenameBytes+4,
		"an overlong suffix is treated as stem, so the cap still binds")

	// A 16-byte extension is still honored; 17 is not.
	ext16 := "." + strings.Repeat("e", 15)
	require.Equal(t, ext16, path.Ext(SanitizeAssetFilename(strings.Repeat("x", 400)+ext16)))
	ext17 := "." + strings.Repeat("e", 16)
	require.NotEqual(t, ext17, path.Ext(SanitizeAssetFilename(strings.Repeat("x", 400)+ext17)))
}

// Truncation is rune-aware: it must never split a multi-byte character and
// leave invalid UTF-8 on the filesystem.
func TestSanitizeAssetFilenameTruncatesOnRuneBoundaries(t *testing.T) {
	for _, r := range []string{"é", "☕", "😀"} {
		t.Run(r, func(t *testing.T) {
			in := strings.Repeat(r, 300) + ".png"
			got := SanitizeAssetFilename(in)
			require.True(t, isValidUTF8(got), "truncation must not split a rune: %q", got)
			require.Equal(t, ".png", path.Ext(got))
			require.Less(t, len(got), 255, "stays inside the filesystem basename limit")
		})
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// An all-hostile name can sanitize down to something empty or separator-only;
// the caller (assets.Store) must be able to detect that, so pin the shape.
func TestSanitizeAssetFilenameDegenerateInputs(t *testing.T) {
	require.Equal(t, "", SanitizeAssetFilename(""))
	require.Equal(t, "", SanitizeAssetFilename("   "))
	require.Equal(t, "", SanitizeAssetFilename("\x00\x01"))
	// "/" becomes "-" then trims to "-": not empty, but also not a separator.
	require.Equal(t, "-", SanitizeAssetFilename("/"))
	require.NotContains(t, SanitizeAssetFilename("/"), "/")
}
