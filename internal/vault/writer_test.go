package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteAtomicReplaces(t *testing.T) {
	p := filepath.Join(t.TempDir(), "n.md")
	require.NoError(t, os.WriteFile(p, []byte("old"), 0o644))
	require.NoError(t, WriteAtomic(p, []byte("new")))
	got, _ := os.ReadFile(p)
	require.Equal(t, "new", string(got))
}

func TestCreateNewNoteCollisionSuffix(t *testing.T) {
	dir := t.TempDir()

	p1, err := CreateNewNote(dir, "Note", []byte("one"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "Note.md"), p1)

	p2, err := CreateNewNote(dir, "Note.md", []byte("two"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "Note 2.md"), p2, "never overwrites; deterministic suffix")

	p3, err := CreateNewNote(dir, "Note", []byte("three"))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "Note 3.md"), p3)

	got, _ := os.ReadFile(p1)
	require.Equal(t, "one", string(got))

	// No temp litter left behind.
	entries, _ := os.ReadDir(dir)
	require.Len(t, entries, 3)
}
