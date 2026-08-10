// Package fetch is the single hardened HTTP client for all outbound
// requests (SPEC §21): SSRF guard on resolved addresses, capped redirects,
// response size, and time. Hostile URLs are the normal case.
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"syscall"
	"time"
)

const (
	connectTimeout = 3 * time.Second
	totalDeadline  = 20 * time.Second
	maxRedirects   = 5

	// MaxHTMLBody / MaxFeedBody cap response bodies BEFORE parsing.
	MaxHTMLBody = 10 << 20
	MaxFeedBody = 15 << 20

	// DefaultMaxDownload caps asset downloads (SPEC §31.3);
	// [vaults.assets] max_download overrides per vault.
	DefaultMaxDownload = 100 << 20
	// downloadDeadline is Download's own wall clock (SPEC §31.3): the 20 s
	// page deadline would make a 100 MiB body unreachable on real links.
	downloadDeadline = 10 * time.Minute

	sniffLen = 512
)

// deniedPrefixes are the address ranges pkms never connects to (SPEC §21).
// v4-mapped/embedded v6 forms are unwrapped before this check.
var deniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), // incl. cloud metadata
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	// NAT64 / 6to4 carry embedded v4 the Unmap below can't see through;
	// deny the whole translation ranges.
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2002::/16"),
}

// DeniedAddr reports whether pkms refuses to connect to ip.
func DeniedAddr(ip netip.Addr) bool {
	ip = ip.Unmap() // ::ffff:169.254.169.254 must be checked as v4
	// IPv4-compatible IPv6 (::/96, deprecated): the classic bypass family
	// that Unmap doesn't cover — re-check the embedded v4. (:: and ::1 fall
	// out as 0.0.0.0 / 0.0.0.1, which 0.0.0.0/8 already denies.)
	if ip.Is6() {
		if b := ip.As16(); allZero(b[:12]) {
			v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
			if DeniedAddr(v4) {
				return true
			}
		}
	}
	for _, p := range deniedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// ssrfControl runs post-DNS-resolution, pre-connect — the only stdlib hook
// that closes the TOCTOU/DNS-rebinding window. It re-runs on every dial,
// so every redirect hop and retry is re-checked.
func ssrfControl(network, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("refusing to connect to unparseable address %q", address)
	}
	if DeniedAddr(ap.Addr()) {
		return fmt.Errorf("refusing to fetch: resolves to private address %s", ap.Addr())
	}
	return nil
}

// Result is one fetched response, body already read under the size cap.
type Result struct {
	Body []byte
	// FinalURL is the post-redirect URL (== requested URL when no redirect).
	FinalURL string
	// Header is the final response's header (Content-Type, ETag, …).
	Header http.Header
	// StatusCode of the final response.
	StatusCode int
}

// Version is stamped into the User-Agent; the CLI sets it from its build
// version at startup (ingesters construct clients without seeing it).
var Version = "dev"

// Client wraps the hardened http.Client. Zero value is not usable — New().
type Client struct {
	http *http.Client
	// dl shares the hardened transport and redirect policy but carries
	// Download's own 10 m deadline (SPEC §31.3) — inherited by
	// construction, so the SSRF dialer re-checks every hop here too.
	dl        *http.Client
	userAgent string
}

// New builds the hardened client.
func New() *Client {
	dialer := &net.Dialer{Timeout: connectTimeout, Control: ssrfControl}
	transport := &http.Transport{
		DialContext:       dialer.DialContext,
		ForceAttemptHTTP2: true,
		Proxy:             nil, // a proxy would bypass the resolved-IP check
	}
	redirects := func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return checkURL(req.URL, via[0].URL)
	}
	return &Client{
		http: &http.Client{
			Transport:     transport,
			Timeout:       totalDeadline,
			CheckRedirect: redirects,
		},
		dl: &http.Client{
			Transport:     transport,
			Timeout:       downloadDeadline,
			CheckRedirect: redirects,
		},
		userAgent: fmt.Sprintf("pkms/%s (+https://github.com/rdegges/pkms)", Version),
	}
}

// checkURL enforces scheme and port policy (SPEC §21): http/https only;
// ports 80/443 always; a non-standard port only when the ORIGINAL request
// carried exactly that port (redirects must not steer to odd ports).
func checkURL(u, original *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing to fetch %s: only http and https URLs are supported", u)
	}
	port := u.Port()
	if port == "" || port == "80" || port == "443" || original.Port() == port {
		return nil
	}
	return fmt.Errorf("refusing to fetch %s: redirect to non-standard port %s", u, port)
}

var errBodyTooLarge = errors.New("response body exceeds the size limit")

// Get fetches url with the size cap maxBody, returning the capped body.
// conditional headers (may be nil) are added verbatim (If-None-Match, …);
// a 304 response returns Result with StatusCode 304 and no body.
func (c *Client) Get(ctx context.Context, rawURL string, maxBody int64, conditional http.Header) (*Result, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("bad URL %q: %w", rawURL, err)
	}
	// The user's own URL may carry any explicit port; redirects away from
	// it are what checkURL restricts (SPEC §21).
	if err := checkURL(u, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	for k, vs := range conditional {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	res := &Result{FinalURL: resp.Request.URL.String(), Header: resp.Header, StatusCode: resp.StatusCode}
	if resp.StatusCode == http.StatusNotModified {
		return res, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %s", rawURL, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	if int64(len(body)) > maxBody {
		return nil, fmt.Errorf("fetch %s: %w (%d MiB)", rawURL, errBodyTooLarge, maxBody>>20)
	}
	res.Body = body
	return res, nil
}

// Download is one spooled fetch (SPEC §31.3): the body streams to an
// OS-temp file, hashed and sniffed on the way through — never
// whole-buffered. The caller owns SpoolPath and must remove it.
type Download struct {
	SpoolPath string
	Size      int64
	SHA256    string // lowercase hex
	Sniff     []byte // first 512 bytes, for MIME dispatch (SPEC §20)
	FinalURL  string
	Header    http.Header
}

// Download fetches rawURL under maxBody (0 → DefaultMaxDownload) onto a
// spool file. An over-cap body is an error, never a truncated asset.
func (c *Client) Download(ctx context.Context, rawURL string, maxBody int64) (*Download, error) {
	if maxBody <= 0 {
		maxBody = DefaultMaxDownload
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("bad URL %q: %w", rawURL, err)
	}
	if err := checkURL(u, u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.dl.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %s", rawURL, resp.Status)
	}

	spool, err := os.CreateTemp("", "pkms-dl-*")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(spool, io.TeeReader(io.LimitReader(resp.Body, maxBody+1), h))
	if err == nil && n > maxBody {
		err = fmt.Errorf("fetch %s: %w (%d MiB)", rawURL, errBodyTooLarge, maxBody>>20)
	}
	if err == nil {
		err = spool.Sync()
	}
	if cerr := spool.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(spool.Name())
		return nil, err
	}

	sniff := make([]byte, sniffLen)
	sn, err := readAtStart(spool.Name(), sniff)
	if err != nil {
		_ = os.Remove(spool.Name())
		return nil, err
	}
	return &Download{
		SpoolPath: spool.Name(),
		Size:      n,
		SHA256:    hex.EncodeToString(h.Sum(nil)),
		Sniff:     sniff[:sn],
		FinalURL:  resp.Request.URL.String(),
		Header:    resp.Header,
	}, nil
}

func readAtStart(path string, buf []byte) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	n, err := io.ReadFull(f, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return n, err
}
