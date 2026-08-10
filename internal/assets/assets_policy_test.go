package assets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---- threshold boundary ------------------------------------------------

// SPEC §31.2 says "≤ threshold → into the vault". Off-by-one here silently
// relocates every asset of exactly the threshold size out of the vault.
func TestStoreThresholdIsInclusive(t *testing.T) {
	body := []byte("0123456789") // 10 bytes

	p := policy(t, 10)
	st, err := p.Store(src("exact.bin", body))
	require.NoError(t, err)
	require.True(t, st.InVault, "size == threshold lands IN the vault")

	p = policy(t, 9)
	st, err = p.Store(src("exact.bin", body))
	require.NoError(t, err)
	require.False(t, st.InVault, "one byte over goes external")
}

// A zero-byte asset is at or under every threshold and must still store.
func TestStoreZeroByteAsset(t *testing.T) {
	p := policy(t, 100)
	st, err := p.Store(src("empty.bin", nil))
	require.NoError(t, err)
	require.True(t, st.InVault)
	require.Equal(t, "Attachments/empty.bin", st.Path)
	fi, err := os.Stat(filepath.Join(p.VaultRoot, "Attachments", "empty.bin"))
	require.NoError(t, err)
	require.Equal(t, int64(0), fi.Size())
}

// ---- containment -------------------------------------------------------

// The profile manifest is trusted-ish, but an attachments dir that escapes
// the vault must never be honored: notes reference it vault-relative, so an
// escape writes outside the synced tree AND produces a dangling wikilink.
func TestStoreRefusesEscapingAttachmentsDir(t *testing.T) {
	for _, dir := range []string{"../outside", "a/../../outside"} {
		t.Run(dir, func(t *testing.T) {
			p := policy(t, 100)
			p.AttachmentsDir = dir
			_, err := p.Store(src("a.png", []byte("x")))
			require.ErrorContains(t, err, "escapes the vault")
		})
	}
}

// A filename that sanitizes to "." or ".." must never resolve to a directory
// operation on the attachments dir itself or its parent.
func TestStoreDegenerateFilenamesStayInsideAttachments(t *testing.T) {
	for _, name := range []string{".", "..", "...", "/", "  ", "\x00"} {
		t.Run("name="+strings.TrimSpace(name), func(t *testing.T) {
			p := policy(t, 100)
			st, err := p.Store(src(name, []byte("payload for "+name)))
			require.NoError(t, err, "a degenerate name must not fail the ingest")
			require.True(t, st.InVault)
			require.True(t, strings.HasPrefix(st.Path, "Attachments/"),
				"stored path must stay under the attachments dir, got %q", st.Path)
			base := strings.TrimPrefix(st.Path, "Attachments/")
			require.NotContains(t, base, "/", "no nesting escape")
			require.NotEqual(t, "..", base)
			require.FileExists(t, filepath.Join(p.VaultRoot, filepath.FromSlash(st.Path)))
		})
	}
}

// ---- filename length under collision -----------------------------------

// The " 2"/" 3" suffix is appended AFTER the 180-byte cap has been applied,
// so a collision on a maximally long name grows past the cap. It must still
// stay well inside the 255-byte filesystem limit.
func TestStoreCollisionOnOverlongNameStaysUnderFilesystemLimit(t *testing.T) {
	p := policy(t, 100)
	long := strings.Repeat("x", 400) + ".pdf"

	st1, err := p.Store(src(long, []byte("one")))
	require.NoError(t, err)
	st2, err := p.Store(src(long, []byte("two")))
	require.NoError(t, err)

	require.NotEqual(t, st1.Path, st2.Path)
	for _, st := range []Stored{st1, st2} {
		base := filepath.Base(st.Path)
		require.Less(t, len(base), 255, "basename must fit the filesystem limit: %d bytes", len(base))
		require.Equal(t, ".pdf", filepath.Ext(base), "the suffix goes BEFORE the extension")
		require.FileExists(t, filepath.Join(p.VaultRoot, filepath.FromSlash(st.Path)))
	}
}

// Unicode names must survive truncation as valid UTF-8 and still be creatable.
func TestStoreUnicodeOverlongName(t *testing.T) {
	p := policy(t, 100)
	st, err := p.Store(src(strings.Repeat("😀", 200)+".png", []byte("x")))
	require.NoError(t, err)
	base := filepath.Base(st.Path)
	require.Less(t, len(base), 255)
	require.Equal(t, ".png", filepath.Ext(base))
	require.NotContains(t, base, "�", "truncation must not split a rune")
	require.FileExists(t, filepath.Join(p.VaultRoot, filepath.FromSlash(st.Path)))
}

// ---- IO failure --------------------------------------------------------

// A reader that fails mid-stream is an execution error (SPEC §31.5: "this
// machine is bad"), and must leave no partial file and no temp litter.
func TestStoreOpenFailureLeavesNothingBehind(t *testing.T) {
	p := policy(t, 100)
	sentinel := errors.New("disk on fire")

	s := src("a.png", []byte("x"))
	s.Open = func() (io.ReadCloser, error) { return nil, sentinel }
	_, err := p.Store(s)
	require.ErrorIs(t, err, sentinel)

	s2 := src("b.png", []byte("x"))
	s2.Open = func() (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(bytes.NewReader([]byte("part")), errReader{sentinel})), nil
	}
	_, err = p.Store(s2)
	require.ErrorIs(t, err, sentinel)

	entries, err := os.ReadDir(filepath.Join(p.VaultRoot, "Attachments"))
	require.NoError(t, err)
	require.Empty(t, entries, "no partial file and no leftover temp file")
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// ---- concurrency -------------------------------------------------------

// Two vaults' ingest runs (or two sources) can store the same attachment at
// once. The safety invariants that must hold on every interleaving: nothing
// is overwritten, and every returned path contains exactly the bytes that
// were asked for.
func TestStoreConcurrentSameNameSameContentIsSafe(t *testing.T) {
	p := policy(t, 1000)
	body := []byte("concurrently stored bytes")
	sum := sha256.Sum256(body)
	wantSHA := hex.EncodeToString(sum[:])

	const n = 16
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := p.Store(src("shared.bin", body))
			paths[i], errs[i] = st.Path, err
		}()
	}
	close(start)
	wg.Wait()

	distinct := map[string]bool{}
	for i := range n {
		require.NoError(t, errs[i])
		require.True(t, strings.HasPrefix(paths[i], "Attachments/"))
		got, err := os.ReadFile(filepath.Join(p.VaultRoot, filepath.FromSlash(paths[i])))
		require.NoError(t, err)
		require.Equal(t, body, got, "a concurrent store must never yield a path with wrong bytes")
		distinct[paths[i]] = true
	}

	// Every file that landed must hash to the requested content — no torn
	// or partial writes are visible under any interleaving.
	entries, err := os.ReadDir(filepath.Join(p.VaultRoot, "Attachments"))
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, strings.HasPrefix(e.Name(), ".pkms-asset-"),
			"no temp file survives: %s", e.Name())
		h, err := hashFile(filepath.Join(p.VaultRoot, "Attachments", e.Name()))
		require.NoError(t, err)
		require.Equal(t, wantSHA, h, "%s holds unexpected bytes", e.Name())
	}
	// §31.2 idempotent reuse must hold under concurrency: every racer
	// converges on ONE path and ONE file (BDFL gate condition 1).
	require.Len(t, distinct, 1, "concurrent identical stores must converge on one path")
	require.Len(t, entries, 1, "exactly one file lands on disk")
}

// Concurrent stores of DIFFERENT content under the same name must each get
// their own path; no store may clobber another's bytes.
func TestStoreConcurrentSameNameDifferentContentNeverOverwrites(t *testing.T) {
	p := policy(t, 1000)
	const n = 8
	bodies := make([][]byte, n)
	for i := range n {
		bodies[i] = []byte(strings.Repeat(string(rune('a'+i)), 20+i))
	}

	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := p.Store(src("clash.bin", bodies[i]))
			paths[i], errs[i] = st.Path, err
		}()
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i := range n {
		require.NoError(t, errs[i])
		require.False(t, seen[paths[i]], "two different payloads landed on the same path %q", paths[i])
		seen[paths[i]] = true
		got, err := os.ReadFile(filepath.Join(p.VaultRoot, filepath.FromSlash(paths[i])))
		require.NoError(t, err)
		require.Equal(t, bodies[i], got, "store %d's bytes were clobbered", i)
	}
}

// The CAS is content-addressed: concurrent stores of identical bytes must
// converge on one blob, and the blob must hold those bytes.
func TestStoreConcurrentCASConverges(t *testing.T) {
	p := policy(t, 0) // everything over threshold, no LocalPath → CAS
	body := []byte("large remote payload")

	const n = 12
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := p.Store(src("blob.bin", body))
			paths[i], errs[i] = st.Path, err
		}()
	}
	close(start)
	wg.Wait()

	for i := range n {
		require.NoError(t, errs[i])
		require.Equal(t, paths[0], paths[i], "CAS paths are the hash — they must all agree")
	}
	entries, err := os.ReadDir(p.CASDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "one blob, no temp litter")
	got, err := os.ReadFile(paths[0])
	require.NoError(t, err)
	require.Equal(t, body, got)
}

// ---- CAS naming --------------------------------------------------------

// The CAS basename is the hash plus the SANITIZED extension: a hostile
// filename must not smuggle a path separator into $XDG_DATA_HOME.
func TestStoreCASNameIsHashPlusSafeExtension(t *testing.T) {
	p := policy(t, 0)
	s := src("../../../evil.tar.gz", []byte("payload"))
	st, err := p.Store(s)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(p.CASDir, s.SHA256+".gz"), st.Path)
	require.Equal(t, p.CASDir, filepath.Dir(st.Path), "the blob stays in the CAS dir")
}

func TestStoreCASNoExtension(t *testing.T) {
	p := policy(t, 0)
	s := src("README", []byte("payload"))
	st, err := p.Store(s)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(p.CASDir, s.SHA256), st.Path)
}

// ---- reference in place ------------------------------------------------

// An over-threshold local file is referenced, never read: the returned path
// must be the user's own absolute path and nothing may be created anywhere.
func TestStoreReferenceInPlaceCopiesNothing(t *testing.T) {
	p := policy(t, 4)
	local := filepath.Join(t.TempDir(), "movie.mov")
	require.NoError(t, os.WriteFile(local, []byte("a long movie file"), 0o644))

	s := src("movie.mov", []byte("a long movie file"))
	s.LocalPath = local
	s.Open = func() (io.ReadCloser, error) {
		t.Fatal("reference-in-place must never open the file")
		return nil, nil
	}
	st, err := p.Store(s)
	require.NoError(t, err)
	require.Equal(t, local, st.Path)
	require.False(t, st.New, "New=false keeps cleanup from deleting the user's own file")
	require.NoDirExists(t, filepath.Join(p.VaultRoot, "Attachments"))
	require.NoDirExists(t, p.CASDir)
}

// New=false on reference-in-place is load-bearing: the pipeline's cleanup
// path deletes only New assets, and deleting a user's source file would be
// data loss. Pinned explicitly because the consequence is unrecoverable.
func TestStoreReferenceInPlaceIsNeverMarkedNew(t *testing.T) {
	p := policy(t, 0)
	local := filepath.Join(t.TempDir(), "keepme.bin")
	require.NoError(t, os.WriteFile(local, []byte("irreplaceable"), 0o644))
	s := src("keepme.bin", []byte("irreplaceable"))
	s.LocalPath = local
	st, err := p.Store(s)
	require.NoError(t, err)
	require.False(t, st.New)
}

// ---- incomplete sources ------------------------------------------------

func TestStoreRejectsEachMissingSourceField(t *testing.T) {
	body := []byte("x")
	full := src("a.bin", body)

	cases := map[string]Source{
		"no sha":    {Filename: "a.bin", Size: 1, Open: full.Open},
		"no reader": {Filename: "a.bin", SHA256: full.SHA256, Size: 1},
		"neg size":  {Filename: "a.bin", SHA256: full.SHA256, Size: -1, Open: full.Open},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			p := policy(t, 100)
			_, err := p.Store(s)
			require.ErrorContains(t, err, "ingester bug")
		})
	}
}
