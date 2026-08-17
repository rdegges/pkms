package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/paths"
)

// The §31.14 compilation cache exists to amortize the per-child wasm
// compile. Its whole contract: a cache problem is only ever the slow
// path — extraction correctness never depends on it.

// A successful extraction populates the cache dir, and a second
// extraction against the populated cache HITS it rather than recompiling.
//
// The hit is asserted without timing: wazero writes an entry only on the
// compile path (engine.CompileModule calls addCompiledModule only after a
// cache miss) and writes it temp+rename, so a miss necessarily replaces
// the file. An unchanged entry set with unchanged mtimes is therefore
// proof the second extraction read the cache instead of recompiling —
// the one thing §31.14 buys, and the thing a key-derivation regression
// (a cache written but never hit) would silently lose while every other
// assertion in this file still passed. The eval scorecard supplies the
// wall-clock numbers.
//
// The two documents differ on purpose: the entry is keyed by module
// content (pdfium.wasm), not by the document, so one entry serves every
// PDF the vault ever ingests. That is what bounds the directory's size.
func TestPDFCachePopulatesAndReuses(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the child inherits os.Environ()

	p := writePDF(t, buildMinimalPDF("cache me once"))
	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "cache me once")

	entries := cacheFiles(t)
	require.Len(t, entries, 1,
		"a successful extraction must leave exactly one compiled module in %s, got %v",
		paths.CacheDir("wazero"), entries)
	before := cacheStamps(t, entries)

	text, err = ExtractPDFText(writePDF(t, buildMinimalPDF("cache me twice")))
	require.NoError(t, err)
	require.Contains(t, text, "cache me twice")

	after := cacheStamps(t, cacheFiles(t))
	require.Equal(t, before, after,
		"the second extraction rewrote the cache — it recompiled instead of reusing (§31.14 bought nothing)")
}

// An unusable cache location (here: the XDG cache base is a FILE, so the
// cache dir cannot be created) falls back to the uncached compile.
func TestPDFCacheUnusableDirFallsBack(t *testing.T) {
	base := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(base, []byte("x"), 0o644))
	t.Setenv("XDG_CACHE_HOME", base)

	text, err := ExtractPDFText(writePDF(t, buildMinimalPDF("no cache today")))
	require.NoError(t, err, "an unusable cache dir must cost speed, never correctness")
	require.Contains(t, text, "no cache today")
}

// Every cached entry corrupted in place: extraction must still succeed —
// either wazero recovers by recompiling, or initPDFiumPool's retry drops
// the cache. Which path ran is deliberately not asserted; the contract
// is only "never fail because of the cache".
func TestPDFCachePoisonedEntriesFallBack(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := ExtractPDFText(writePDF(t, buildMinimalPDF("seed the cache")))
	require.NoError(t, err)
	entries := cacheFiles(t)
	require.NotEmpty(t, entries, "premise: the cache must be populated before poisoning")
	for _, f := range entries {
		require.NoError(t, os.WriteFile(f, []byte("poisoned garbage, not compiled code"), 0o644))
	}

	text, err := ExtractPDFText(writePDF(t, buildMinimalPDF("still alive")))
	require.NoError(t, err, "a poisoned cache must cost speed, never correctness")
	require.Contains(t, text, "still alive")
}

// Corruption that gets PAST the cheap checks. TestPDFCachePoisonedEntries
// FallBack overwrites the whole entry, so wazero rejects it on byte 0 (the
// WAZEVO magic) — the easiest possible failure to survive. Bit rot is the
// hard end of the same class: one flipped byte inside the ~19 MB of
// machine code, length preserved, so the header and version validate, the
// segment is mmapped, and only the trailing CRC catches it. Everything
// pkms does about it happens after the engine has already committed to the
// entry, and it must still be only the slow path.
func TestPDFCacheBitRotInEntryFallsBack(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := ExtractPDFText(writePDF(t, buildMinimalPDF("seed the cache")))
	require.NoError(t, err)
	entries := cacheFiles(t)
	require.Len(t, entries, 1, "premise: exactly one entry to corrupt")

	b, err := os.ReadFile(entries[0])
	require.NoError(t, err)
	require.Greater(t, len(b), 1024, "premise: the entry holds a compiled module")
	b[len(b)/2] ^= 0xff
	require.NoError(t, os.WriteFile(entries[0], b, 0o600))

	text, err := ExtractPDFText(writePDF(t, buildMinimalPDF("still alive")))
	require.NoError(t, err, "a bit-rotted cache must cost speed, never correctness")
	require.Contains(t, text, "still alive")
}

// The cache holds machine code, and the ONLY thing standing between a
// wazero upgrade and executing machine code an older wazero compiled is
// the version string wazero stamps into each entry (§31.14 "an engine or
// runtime upgrade can never reuse stale machine code"). The version-named
// subdirectory is the outer half of that guard; this is the inner half,
// and it is the half that still holds when the directory name does not
// change. If an upgrade ever drops the stamp, this test fails and the
// §31.14 consistency claim needs rewriting before the upgrade lands.
//
// Simulated by rewriting the stamp in place, same length, so nothing else
// about the entry moves: wazero must treat it as stale and replace it
// with one this build wrote, not deserialize it.
func TestPDFCacheForeignVersionEntryIsRecompiled(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := ExtractPDFText(writePDF(t, buildMinimalPDF("seed the cache")))
	require.NoError(t, err)
	entries := cacheFiles(t)
	require.Len(t, entries, 1)

	// Entry layout (wazero serializeCompiledModule): magic | len(version) |
	// version | ...compiled module.
	const magic = "WAZEVO"
	b, err := os.ReadFile(entries[0])
	require.NoError(t, err)
	require.Equal(t, magic, string(b[:len(magic)]), "premise: the entry starts with wazero's magic")
	n := int(b[len(magic)])
	require.Positive(t, n, "premise: the entry carries a version stamp")
	lo, hi := len(magic)+1, len(magic)+1+n
	ours := string(b[lo:hi])

	foreign := []byte(strings.Repeat("Z", n))
	require.NotEqual(t, ours, string(foreign), "premise: the forged stamp differs from ours")
	copy(b[lo:hi], foreign)
	require.NoError(t, os.WriteFile(entries[0], b, 0o600))

	text, err := ExtractPDFText(writePDF(t, buildMinimalPDF("still alive")))
	require.NoError(t, err)
	require.Contains(t, text, "still alive")

	after, err := os.ReadFile(entries[0])
	require.NoError(t, err)
	require.Equal(t, ours, string(after[lo:hi]),
		"a foreign-version entry survived: this build would execute machine code it did not compile")
}

// §31.14 claims concurrent extraction children share the cache directory
// safely. TestExtractPDFTextIsSafeInParallel already runs extractions
// concurrently, but by the time it runs the cache is warm — every child
// only READS. The race the claim is actually about is the cold start: N
// children compiling at once, each racing to write the same
// content-keyed entry through wazero's temp+fsync+rename. All must
// succeed with their own document's text, and the directory must settle
// on one entry with no half-written temp file left behind (a vault
// ingesting a directory of PDFs cold-starts exactly this way).
func TestPDFCacheConcurrentColdStartSettlesOnOneEntry(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// Deadline headroom for -race: instrumented, the uncached compile every
	// child here pays takes ~13s against the production 20s pdfTimeout, and
	// these children compile simultaneously (pattern from
	// TestExtractPDFTextIsSafeInParallel).
	old := pdfTimeout
	pdfTimeout = 4 * time.Minute
	t.Cleanup(func() { pdfTimeout = old })

	const n = 4
	docs := make([]string, n)
	for i := range docs {
		docs[i] = writePDF(t, buildMinimalPDF("cold start "+strconv.Itoa(i)))
	}
	require.Empty(t, cacheFiles(t), "premise: the cache starts cold")

	var wg sync.WaitGroup
	got := make([]string, n)
	errs := make([]error, n)
	for i := range docs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], errs[i] = ExtractPDFText(docs[i])
		}(i)
	}
	wg.Wait()

	for i := range docs {
		require.NoError(t, errs[i], "cold-start extraction %d failed", i)
		require.Contains(t, got[i], "cold start "+strconv.Itoa(i),
			"cold-start extraction %d got another document's text: %q", i, got[i])
	}

	entries := cacheFiles(t)
	require.Len(t, entries, 1,
		"%d cold children left %d files in the cache dir: %v", n, len(entries), entries)
	require.NotContains(t, entries[0], ".tmp",
		"a temp file survived the rename race — the cache dir grows without bound")
}

// The §31.14 threat note argues the cache adds no trust boundary. That
// holds only while the directory stays private: it is executable machine
// code the extraction child mmaps and runs, so another local account
// being able to write there would be a new way in. Pinned because the
// permissions are wazero's choice, not pkms', and pkms is the one making
// the claim.
func TestPDFCacheDirIsPrivateToTheUser(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	_, err := ExtractPDFText(writePDF(t, buildMinimalPDF("check the perms")))
	require.NoError(t, err)

	// WalkDir surfaces a missing root as an error, so a cache that was never
	// created fails this test rather than passing it vacuously.
	require.NoError(t, filepath.WalkDir(paths.CacheDir("wazero"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		require.Zero(t, st.Mode().Perm()&0o077,
			"%s is group- or world-accessible (%v): compiled machine code must stay private", p, st.Mode().Perm())
		return nil
	}))
}

// cacheFiles lists regular files under the pkms wazero cache dir.
func cacheFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(paths.CacheDir("wazero"), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// cacheStamps fingerprints cache entries by path, size and mtime — enough
// to tell a read from a rewrite, since wazero replaces an entry wholesale
// (temp + rename) rather than updating it in place.
func cacheStamps(t *testing.T, files []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(files))
	for _, f := range files {
		st, err := os.Stat(f)
		require.NoError(t, err)
		out[f] = fmt.Sprintf("%d@%d", st.Size(), st.ModTime().UnixNano())
	}
	return out
}
