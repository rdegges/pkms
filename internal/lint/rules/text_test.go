package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/lint"
	"github.com/rdegges/pkms/internal/profile"
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

// A rule that is registered but not enabled by a profile is a gate that
// never fires. Both shipped profiles must run it with no `--rules` flag.
func TestNoteValidTextRunsByDefaultOnBothProfiles(t *testing.T) {
	for _, name := range []string{"para", "rdegges"} {
		t.Run(name, func(t *testing.T) {
			prof, err := profile.Load(name)
			require.NoError(t, err)
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "Resources"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "Resources", "corrupt.md"),
				[]byte("---\ntitle: x\n---\n\nbefore\x00after\n"), 0o644))
			ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
			require.NoError(t, err)

			// No `only` filter: exactly what `pkms lint` runs.
			fs, err := lint.Run(ix, prof, nil, nil)
			require.NoError(t, err)
			got := byRule(fs)["note-valid-text"]
			require.Len(t, got, 1, "the rule must be on by default under %s: %+v", name, fs)
			require.Equal(t, lint.Error, got[0].Severity)
		})
	}
}

// False positives would make the check unusable: binaries live in the
// attachments dir and beside notes, and only markdown notes are scanned.
func TestNoteValidTextIgnoresNonNotes(t *testing.T) {
	prof, err := profile.Load("rdegges")
	require.NoError(t, err)
	root := t.TempDir()
	binary := "\x00\x01\x02\xff\xfe PNG-ish bytes\n"
	files := map[string]string{
		prof.Attachments + "/photo.png":     binary,
		prof.Attachments + "/misnamed.md":   binary, // inside attachments: never note-parsed
		"Resources/Personal/data.bin":       binary,
		"Resources/Personal/.hidden.md":     binary, // dotfiles are not notes
		"Resources/Personal/legit.md":       "---\ntitle: ok\n---\n\nfine\n",
		"Resources/Personal/no-extension":   binary,
		"Resources/Personal/trailing.md.md": "clean\n",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	ix, err := vault.BuildIndex(root, vault.WalkOptions{AttachmentsDir: prof.Attachments})
	require.NoError(t, err)

	fs, err := lint.Run(ix, prof, nil, []string{"note-valid-text"})
	require.NoError(t, err)
	require.Empty(t, byRule(fs)["note-valid-text"], "only markdown notes are scanned: %+v", fs)
}

// A binary file misnamed .md must produce at most two findings (one per
// defect kind) with the real counts — never one finding per bad byte.
func TestNoteValidTextCapsFindingsPerNote(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("\x00\x1b\xff\xfe")
	}
	fs := run(t, map[string]string{"Resources/Personal/binary.md": b.String()}, "note-valid-text")

	got := byRule(fs)["note-valid-text"]
	require.Len(t, got, 2, "one finding per defect kind, not per byte: %d findings", len(got))
	var utf8Msg, ctrlMsg string
	for _, f := range got {
		require.Equal(t, "Resources/Personal/binary.md", f.Path)
		require.Equal(t, lint.Error, f.Severity)
		require.False(t, f.Fixable, "stripping bytes is destructive; repair is the owner's call")
		if strings.Contains(f.Message, "UTF-8") {
			utf8Msg = f.Message
		} else {
			ctrlMsg = f.Message
		}
	}
	require.Contains(t, utf8Msg, "1000 invalid byte(s)", "both \\xff and \\xfe are counted, 500 times each")
	require.Contains(t, ctrlMsg, "1000 control byte(s)")
	require.Contains(t, ctrlMsg, "0x00")
}
