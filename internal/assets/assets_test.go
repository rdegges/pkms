package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func src(name string, body []byte) Source {
	sum := sha256.Sum256(body)
	return Source{
		Filename: name,
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(body)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		},
	}
}

func policy(t *testing.T, threshold int64) Policy {
	t.Helper()
	return Policy{
		VaultRoot:      t.TempDir(),
		AttachmentsDir: "Attachments",
		Threshold:      threshold,
		CASDir:         filepath.Join(t.TempDir(), "cas"),
	}
}

// The §31.2 placement decision table.
func TestStorePlacement(t *testing.T) {
	body := []byte("0123456789") // 10 bytes

	t.Run("at or under threshold → in-vault", func(t *testing.T) {
		p := policy(t, 10)
		st, err := p.Store(src("doc.pdf", body))
		require.NoError(t, err)
		require.True(t, st.InVault)
		require.True(t, st.New)
		require.Equal(t, "Attachments/doc.pdf", st.Path)
		require.FileExists(t, filepath.Join(p.VaultRoot, "Attachments", "doc.pdf"))
	})

	t.Run("over threshold, remote → CAS", func(t *testing.T) {
		p := policy(t, 9)
		s := src("doc.pdf", body)
		st, err := p.Store(s)
		require.NoError(t, err)
		require.False(t, st.InVault)
		require.True(t, st.New)
		require.Equal(t, filepath.Join(p.CASDir, s.SHA256+".pdf"), st.Path)
		require.FileExists(t, st.Path)
	})

	t.Run("over threshold, local → referenced in place", func(t *testing.T) {
		p := policy(t, 9)
		local := filepath.Join(t.TempDir(), "big.mov")
		require.NoError(t, os.WriteFile(local, body, 0o644))
		s := src("big.mov", body)
		s.LocalPath = local
		st, err := p.Store(s)
		require.NoError(t, err)
		require.False(t, st.InVault)
		require.False(t, st.New, "nothing written — the user's file is the asset")
		require.Equal(t, local, st.Path)
		require.NoDirExists(t, p.CASDir, "no copy anywhere")
	})
}

func TestStoreIdempotentReuse(t *testing.T) {
	p := policy(t, 100)
	body := []byte("same content")

	st1, err := p.Store(src("a.png", body))
	require.NoError(t, err)
	require.True(t, st1.New)

	st2, err := p.Store(src("a.png", body))
	require.NoError(t, err)
	require.False(t, st2.New, "identical name+content reuses the file")
	require.Equal(t, st1.Path, st2.Path)

	entries, err := os.ReadDir(filepath.Join(p.VaultRoot, "Attachments"))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no duplicate copies")
}

func TestStoreCollisionSuffix(t *testing.T) {
	p := policy(t, 100)

	st1, err := p.Store(src("a.png", []byte("content one")))
	require.NoError(t, err)
	require.Equal(t, "Attachments/a.png", st1.Path)

	st2, err := p.Store(src("a.png", []byte("content TWO")))
	require.NoError(t, err)
	require.Equal(t, "Attachments/a 2.png", st2.Path, "same name, different content → deterministic suffix before the extension")

	st3, err := p.Store(src("a.png", []byte("content THREE")))
	require.NoError(t, err)
	require.Equal(t, "Attachments/a 3.png", st3.Path)

	// Re-storing content TWO reuses its suffixed home.
	st4, err := p.Store(src("a.png", []byte("content TWO")))
	require.NoError(t, err)
	require.False(t, st4.New)
	require.Equal(t, "Attachments/a 2.png", st4.Path)
}

func TestStoreCASReuse(t *testing.T) {
	p := policy(t, 0)
	body := []byte("big remote thing")

	st1, err := p.Store(src("x.bin", body))
	require.NoError(t, err)
	require.True(t, st1.New)

	st2, err := p.Store(src("renamed.bin", body))
	require.NoError(t, err)
	require.False(t, st2.New, "CAS is content-addressed: same bytes → same blob")
	require.Equal(t, st1.Path, st2.Path)
}

func TestStoreHostileFilename(t *testing.T) {
	p := policy(t, 100)
	st, err := p.Store(src("../../evil[[x]]#|name.png", []byte("x")))
	require.NoError(t, err)
	require.True(t, st.InVault)
	require.True(t, strings.HasPrefix(st.Path, "Attachments/"), "sanitized name stays in the attachments dir")
	require.NotContains(t, st.Path[len("Attachments/"):], "/", "no path separators survive")
	require.NotContains(t, st.Path, "[[", "no wikilink syntax survives")
	require.True(t, strings.HasSuffix(st.Path, ".png"), "extension survives sanitizing")
}

func TestStoreOverlongFilenameKeepsExtension(t *testing.T) {
	p := policy(t, 100)
	long := strings.Repeat("x", 400) + ".pdf"
	st, err := p.Store(src(long, []byte("x")))
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(st.Path, ".pdf"), "truncation preserves the extension")
	require.LessOrEqual(t, len(filepath.Base(st.Path)), 180)
}

func TestStoreEmptyFilenameFallsBackToHash(t *testing.T) {
	p := policy(t, 100)
	s := src("", []byte("nameless"))
	st, err := p.Store(s)
	require.NoError(t, err)
	require.Equal(t, "Attachments/"+s.SHA256[:12], st.Path)
}

func TestStoreSymlinkedAttachmentsEscapeRefused(t *testing.T) {
	p := policy(t, 100)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(p.VaultRoot, "Attachments")))

	_, err := p.Store(src("a.png", []byte("x")))
	require.ErrorContains(t, err, "escapes the vault")
	entries, _ := os.ReadDir(outside)
	require.Empty(t, entries, "nothing written through the symlink")
}

func TestStoreCASRefusesSquatter(t *testing.T) {
	// The CAS name is the hash, so existence normally proves identity —
	// but only a REGULAR file counts. A symlink or dir squatting on the
	// name must be an error, never a ledger entry (BDFL gate condition 2).
	body := []byte("cas squatter target bytes")

	t.Run("symlink", func(t *testing.T) {
		p := policy(t, 0)
		s := src("x.bin", body)
		require.NoError(t, os.MkdirAll(p.CASDir, 0o755))
		require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(p.CASDir, s.SHA256+".bin")))
		_, err := p.Store(s)
		require.ErrorContains(t, err, "not a regular file")
	})

	t.Run("dir", func(t *testing.T) {
		p := policy(t, 0)
		s := src("x.bin", body)
		require.NoError(t, os.MkdirAll(filepath.Join(p.CASDir, s.SHA256+".bin"), 0o755))
		_, err := p.Store(s)
		require.ErrorContains(t, err, "not a regular file")
	})
}

func TestStoreNoAttachmentsDirConfigured(t *testing.T) {
	p := policy(t, 100)
	p.AttachmentsDir = ""
	_, err := p.Store(src("a.png", []byte("x")))
	require.ErrorContains(t, err, "no attachments dir")
}

func TestStoreRejectsIncompleteSource(t *testing.T) {
	p := policy(t, 100)
	_, err := p.Store(Source{Filename: "x.bin"})
	require.ErrorContains(t, err, "ingester bug")
}

func TestStoreNeverOverwrites(t *testing.T) {
	// Directly exercises the link-based no-overwrite finalization: a
	// squatter file appearing between Lstat and link must survive.
	p := policy(t, 100)
	dir := filepath.Join(p.VaultRoot, "Attachments")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.png"), []byte("squatter"), 0o644))

	st, err := p.Store(src("a.png", []byte("new content")))
	require.NoError(t, err)
	require.Equal(t, "Attachments/a 2.png", st.Path)
	got, err := os.ReadFile(filepath.Join(dir, "a.png"))
	require.NoError(t, err)
	require.Equal(t, []byte("squatter"), got, "existing file untouched")
}

func TestStoreManyDistinctSameName(t *testing.T) {
	p := policy(t, 1000)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		st, err := p.Store(src("n.dat", []byte(fmt.Sprintf("content %d", i))))
		require.NoError(t, err)
		require.False(t, seen[st.Path], "each distinct content gets a distinct path")
		seen[st.Path] = true
	}
}
