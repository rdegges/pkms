package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/vault"
)

// Over-cap notes are indexed without their bytes (SPEC §14) — the rule must
// stream-scan them from disk, or a 10MiB+1 corrupt file passes blind (§33).
func TestNoteValidTextScansOversizedNotes(t *testing.T) {
	huge := strings.Repeat("a", vault.MaxBodyParseSize) + "\x00\n"
	ix, prof, root := buildVault(t, map[string]string{"Resources/Personal/huge.md": huge})
	require.True(t, ix.Notes["Resources/Personal/huge.md"].TooLarge,
		"premise: the note must take the over-cap path")

	fs, err := lint.Run(ix, prof, nil, []string{"note-valid-text"})
	require.NoError(t, err)
	got := byRule(fs)["note-valid-text"]
	require.Len(t, got, 1, "the NUL in the over-cap note must be found: %v", fs)
	require.Contains(t, got[0].Message, "0x00")

	// An over-cap note that cannot be read cannot be proven valid: the rule
	// must fail closed with an error finding, never silently pass.
	require.NoError(t, os.Remove(filepath.Join(root, "Resources", "Personal", "huge.md")))
	fs, err = lint.Run(ix, prof, nil, []string{"note-valid-text"})
	require.NoError(t, err)
	got = byRule(fs)["note-valid-text"]
	require.Len(t, got, 1)
	require.Contains(t, got[0].Message, "could not read note")
	require.Equal(t, lint.Error, got[0].Severity)
}

func TestNoteValidTextRejectsControlBytes(t *testing.T) {
	fs := run(t, map[string]string{
		"Resources/Personal/nul.md":  "---\ntitle: x\n---\n\nbefore\x00after\n",
		"Resources/Personal/esc.md":  "line one\nred \x1b[31mtext\n",
		"Resources/Personal/cr.md":   "shown\rhidden\n",
		"Resources/Personal/good.md": "col\tumns\r\nemoji 🎉 fine\r\n",
	}, "note-valid-text")

	got := byRule(fs)["note-valid-text"]
	byPath := map[string]lint.Finding{}
	for _, f := range got {
		byPath[f.Path] = f
	}

	require.Len(t, got, 3, "exactly the three seeded notes must be flagged: %v", got)
	require.Contains(t, byPath["Resources/Personal/nul.md"].Message, "0x00")
	require.Equal(t, 5, byPath["Resources/Personal/nul.md"].Line)
	require.Contains(t, byPath["Resources/Personal/esc.md"].Message, "0x1B")
	require.Equal(t, 2, byPath["Resources/Personal/esc.md"].Line)
	require.Contains(t, byPath["Resources/Personal/cr.md"].Message, "0x0D")
	require.NotContains(t, byPath, "Resources/Personal/good.md")
	for _, f := range got {
		require.Equal(t, lint.Error, f.Severity)
		require.False(t, f.Fixable)
	}
}

func TestNoteValidTextRejectsInvalidUTF8(t *testing.T) {
	fs := run(t, map[string]string{
		"Resources/Personal/bad.md": "---\ntitle: x\n---\n\nlatin-1 caf\xe9 body\n",
	}, "note-valid-text")

	got := byRule(fs)["note-valid-text"]
	require.Len(t, got, 1)
	require.Contains(t, got[0].Message, "not valid UTF-8")
	require.Equal(t, 5, got[0].Line)
	require.Equal(t, lint.Error, got[0].Severity)
}

func TestNoteValidTextCleanVaultIsSilent(t *testing.T) {
	fs := run(t, map[string]string{
		"Resources/Personal/a.md": "---\ntitle: ok\n---\n\nplain 日本語 body\n",
		"Resources/Personal/b.md": "crlf line\r\nwith\ttab\r\n",
	}, "note-valid-text")
	require.Empty(t, byRule(fs)["note-valid-text"])
}
