package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/rdegges/pkms/internal/paths"
)

// The §31.14 compilation cache exists to amortize the per-child wasm
// compile. Its whole contract: a cache problem is only ever the slow
// path — extraction correctness never depends on it.

// A successful extraction populates the cache dir, and a second
// extraction against the populated cache still succeeds (the fast path
// exists; the eval scorecard is the timing evidence).
func TestPDFCachePopulatesAndReuses(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // the child inherits os.Environ()

	p := writePDF(t, buildMinimalPDF("cache me once"))
	text, err := ExtractPDFText(p)
	require.NoError(t, err)
	require.Contains(t, text, "cache me once")

	entries := cacheFiles(t)
	require.NotEmpty(t, entries, "a successful extraction must populate %s", paths.CacheDir("wazero"))

	text, err = ExtractPDFText(writePDF(t, buildMinimalPDF("cache me twice")))
	require.NoError(t, err)
	require.Contains(t, text, "cache me twice")
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
