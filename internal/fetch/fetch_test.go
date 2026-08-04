package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeniedAddrTable(t *testing.T) {
	denied := []string{
		"127.0.0.1", "10.0.0.5", "172.16.9.9", "192.168.1.1",
		"169.254.169.254", // cloud metadata
		"100.64.0.1", "0.0.0.0", "224.0.0.1", "255.255.255.255",
		"::1", "::", "fe80::1", "fc00::1", "ff02::1",
		"::ffff:169.254.169.254", // v4-mapped bypass
		"::ffff:10.0.0.5",
		"64:ff9b::a00:5", // NAT64-embedded 10.0.0.5
		"2002:a00:5::1",  // 6to4
	}
	for _, s := range denied {
		require.True(t, DeniedAddr(netip.MustParseAddr(s)), "must deny %s", s)
	}
	allowed := []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		require.False(t, DeniedAddr(netip.MustParseAddr(s)), "must allow %s", s)
	}
}

func TestGetRefusesLoopback(t *testing.T) {
	// httptest listens on 127.0.0.1 — the guard must refuse to connect.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	_, err := New("test").Get(context.Background(), srv.URL, MaxHTMLBody, nil)
	require.ErrorContains(t, err, "private address")
}

// testClient disables only the SSRF dial check so httptest (loopback)
// works; every other control (redirects, ports, caps) stays identical.
func testClient() *Client {
	c := New("test")
	tr := c.http.Transport.(*http.Transport)
	tr.DialContext = nil // default dialer, no Control hook
	return c
}

func TestGetRefusesNonHTTPScheme(t *testing.T) {
	_, err := testClient().Get(context.Background(), "file:///etc/passwd", MaxHTMLBody, nil)
	require.ErrorContains(t, err, "only http and https")

	_, err = testClient().Get(context.Background(), "gopher://example.com/", MaxHTMLBody, nil)
	require.ErrorContains(t, err, "only http and https")
}

func TestGetCapsBodySize(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()

	_, err := testClient().Get(context.Background(), srv.URL, 1024, nil)
	require.ErrorContains(t, err, "exceeds the size limit")

	res, err := testClient().Get(context.Background(), srv.URL, 8192, nil)
	require.NoError(t, err)
	require.Len(t, res.Body, 4096)
}

func TestGetCapsRedirects(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+r.URL.Path+"x", http.StatusFound)
	}))
	defer srv.Close()

	_, err := testClient().Get(context.Background(), srv.URL, MaxHTMLBody, nil)
	require.ErrorContains(t, err, "stopped after 5 redirects")
}

func TestGetRefusesRedirectToOddPort(t *testing.T) {
	// The initial explicit port is allowed; a redirect that CHANGES to a
	// different non-standard port is refused.
	var target *httptest.Server
	target = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer srv.Close()

	_, err := testClient().Get(context.Background(), srv.URL, MaxHTMLBody, nil)
	require.ErrorContains(t, err, "non-standard port")
}

func TestGetFollowsSamePortRedirect(t *testing.T) {
	var srv *httptest.Server
	hits := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, srv.URL+"/final", http.StatusFound)
			return
		}
		hits++
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	res, err := testClient().Get(context.Background(), srv.URL+"/start", MaxHTMLBody, nil)
	require.NoError(t, err)
	require.Equal(t, 1, hits)
	require.Equal(t, srv.URL+"/final", res.FinalURL)
	require.Equal(t, "ok", string(res.Body))
}

func TestGetConditional304(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, "feed")
	}))
	defer srv.Close()

	c := testClient()
	res, err := c.Get(context.Background(), srv.URL, MaxFeedBody, nil)
	require.NoError(t, err)
	require.Equal(t, `"v1"`, res.Header.Get("ETag"))

	cond := http.Header{"If-None-Match": []string{`"v1"`}}
	res, err = c.Get(context.Background(), srv.URL, MaxFeedBody, cond)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotModified, res.StatusCode)
	require.Empty(t, res.Body)
}

func TestGetHTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := testClient().Get(context.Background(), srv.URL, MaxHTMLBody, nil)
	require.ErrorContains(t, err, "HTTP 404")
}
