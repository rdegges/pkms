package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// spoolJail points os.CreateTemp("") at a private dir so a test can prove
// Download leaves no orphaned spool behind on its failure paths (SPEC §31.3
// says a crash leaks to the OS temp dir; a clean error must not).
func spoolJail(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

func requireNoSpools(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "pkms-dl-*"))
	require.NoError(t, err)
	require.Empty(t, left, "a failed Download must not leave a spool file behind")
}

func TestDownloadSpoolsHashesAndSniffs(t *testing.T) {
	jail := spoolJail(t)
	body := append([]byte("%PDF-1.5\n"), []byte(strings.Repeat("b", 2000))...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL+"/doc.pdf", 0)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()

	require.Equal(t, int64(len(body)), dl.Size)
	sum := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(sum[:]), dl.SHA256, "hash is computed over the streamed bytes")
	require.Len(t, dl.Sniff, sniffLen, "sniff is the first 512 bytes (SPEC §20)")
	require.Equal(t, body[:sniffLen], dl.Sniff)
	require.Equal(t, srv.URL+"/doc.pdf", dl.FinalURL)
	require.Equal(t, "application/pdf", dl.Header.Get("Content-Type"))

	spooled, err := os.ReadFile(dl.SpoolPath)
	require.NoError(t, err)
	require.Equal(t, body, spooled, "the spool holds the whole body verbatim")
	require.Equal(t, jail, filepath.Dir(dl.SpoolPath), "spool lives in the OS temp dir, not the vault")
}

// A body shorter than the sniff window must not be padded: DetectContentType
// over a 512-byte zero-padded buffer misclassifies short text as binary.
func TestDownloadSniffShorterThanWindow(t *testing.T) {
	spoolJail(t)
	body := []byte("hello there\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL, 0)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()
	require.Equal(t, body, dl.Sniff)
	require.Equal(t, int64(len(body)), dl.Size)
}

func TestDownloadEmptyBody(t *testing.T) {
	spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL, 0)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()
	require.Equal(t, int64(0), dl.Size)
	require.Empty(t, dl.Sniff)
	empty := sha256.Sum256(nil)
	require.Equal(t, hex.EncodeToString(empty[:]), dl.SHA256)
}

// SPEC §31.3: "An over-cap download aborts with an execution error, never a
// truncated asset."
func TestDownloadOverCapAbortsAndCleansUp(t *testing.T) {
	jail := spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL, 1024)
	require.Error(t, err)
	require.Nil(t, dl, "no truncated asset is ever returned")
	require.ErrorContains(t, err, "exceeds the size limit")
	requireNoSpools(t, jail)
}

// The cap is inclusive: exactly maxBody bytes is not over-cap.
func TestDownloadExactlyAtCapSucceeds(t *testing.T) {
	spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL, 1024)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()
	require.Equal(t, int64(1024), dl.Size)
}

func TestDownloadZeroMaxUsesDefault(t *testing.T) {
	require.Equal(t, int64(100<<20), int64(DefaultMaxDownload),
		"SPEC §31.3 pins the asset download cap at 100 MiB")
	spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("small"))
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL, 0)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()
	require.Equal(t, int64(5), dl.Size)
}

func TestDownloadHTTPErrorStatusLeavesNoSpool(t *testing.T) {
	jail := spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := testClient().Download(context.Background(), srv.URL, 0)
	require.ErrorContains(t, err, "HTTP 404")
	requireNoSpools(t, jail)
}

// The SSRF dialer and the scheme/port policy are inherited by construction
// (SPEC §31.3) — Download must not be a hole around the hardened client.
func TestDownloadRefusesLoopback(t *testing.T) {
	spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	_, err := New().Download(context.Background(), srv.URL, 0)
	require.ErrorContains(t, err, "private address")
}

func TestDownloadRefusesNonHTTPScheme(t *testing.T) {
	spoolJail(t)
	_, err := testClient().Download(context.Background(), "file:///etc/passwd", 0)
	require.ErrorContains(t, err, "only http and https")
}

func TestDownloadCapsRedirects(t *testing.T) {
	jail := spoolJail(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()

	_, err := testClient().Download(context.Background(), srv.URL, 0)
	require.ErrorContains(t, err, "stopped after 5 redirects")
	requireNoSpools(t, jail)
}

func TestDownloadRefusesRedirectToOddPort(t *testing.T) {
	jail := spoolJail(t)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	_, err := testClient().Download(context.Background(), srv.URL, 0)
	require.ErrorContains(t, err, "non-standard port")
	requireNoSpools(t, jail)
}

func TestDownloadRecordsFinalURLAfterRedirect(t *testing.T) {
	spoolJail(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/final.bin", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	dl, err := testClient().Download(context.Background(), srv.URL+"/start", 0)
	require.NoError(t, err)
	defer func() { _ = os.Remove(dl.SpoolPath) }()
	require.Equal(t, srv.URL+"/final.bin", dl.FinalURL,
		"the asset filename is derived from the FINAL URL path")
}

func TestDownloadCancelledContextLeavesNoSpool(t *testing.T) {
	jail := spoolJail(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testClient().Download(ctx, srv.URL, 0)
	require.Error(t, err)
	requireNoSpools(t, jail)
}

func TestDownloadRejectsUnparseableURL(t *testing.T) {
	spoolJail(t)
	_, err := testClient().Download(context.Background(), "http://[::1", 0)
	require.Error(t, err)
}
