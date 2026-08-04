package imap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rdegges/pkms/internal/secrets"
)

// defaultAuthURL is Google's authorization endpoint; overridable per
// ingester with auth_url for non-Google XOAUTH2 providers.
const defaultAuthURL = "https://accounts.google.com/o/oauth2/v2/auth"

// gmailScope is the full-IMAP scope — the Gmail API's narrower scopes do
// not cover IMAP access.
const gmailScope = "https://mail.google.com/"

// Authorize runs the one-time interactive OAuth flow for an imap source
// (SPEC §24): loopback redirect (Google's device-code flow does not allow
// the mail scope), then stores the refresh token in the OS keyring.
// in/out are the user's terminal; prompts go to out, answers come from in.
func Authorize(ctx context.Context, cfgTable map[string]any, vaultName, sourceName string, in io.Reader, out io.Writer) error {
	m, err := New(cfgTable)
	if err != nil {
		return err
	}
	if m.cfg.Auth != "xoauth2" {
		return fmt.Errorf(`ingester %q uses auth = %q; pkms auth is only for auth = "xoauth2" sources`, sourceName, m.cfg.Auth)
	}
	source := "imap:" + sourceName

	clientID, clientSecret, err := oauthClient(vaultName, source, sourceName, in, out)
	if err != nil {
		return err
	}

	// Loopback receiver: Google's desktop-app flow redirects here.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	redirectURI := fmt.Sprintf("http://%s/", ln.Addr().String())

	authURL := buildAuthURL(m.cfg.AuthURL, clientID, redirectURI)
	fmt.Fprintf(out, `Open this URL in your browser and approve access for %s:

  %s

Waiting for the redirect on %s (5-minute timeout)…
`, m.cfg.Username, authURL, redirectURI)

	code, err := waitForCode(ctx, ln)
	if err != nil {
		return err
	}

	refresh, err := exchangeCode(ctx, m.cfg.TokenURL, clientID, clientSecret, code, redirectURI)
	if err != nil {
		return err
	}
	if err := secrets.Store(vaultName, source, secrets.OAuthRefreshTok, refresh); err != nil {
		return fmt.Errorf("store refresh token in the OS keyring: %w", err)
	}
	fmt.Fprintf(out, "Authorized. The refresh token is stored in the OS keyring (account %q).\n",
		secrets.Account(vaultName, source, secrets.OAuthRefreshTok))
	fmt.Fprintln(out, "Heads-up: if the OAuth consent screen is in Testing mode, this token dies in 7 days — see docs/OAUTH-GMAIL.md.")
	return nil
}

// oauthClient resolves or interactively collects + stores the client
// id/secret pair.
func oauthClient(vaultName, source, sourceName string, in io.Reader, out io.Writer) (string, string, error) {
	clientID, err := secrets.Resolve(vaultName, source, sourceName, secrets.OAuthClientID, nil)
	if err != nil {
		fmt.Fprint(out, "OAuth client ID (from your Google Cloud project — see docs/OAUTH-GMAIL.md): ")
		clientID, err = readLine(in)
		if err != nil || clientID == "" {
			return "", "", fmt.Errorf("an OAuth client ID is required: %w", err)
		}
		if serr := secrets.Store(vaultName, source, secrets.OAuthClientID, clientID); serr != nil {
			return "", "", fmt.Errorf("store client ID in the OS keyring: %w", serr)
		}
	}
	clientSecret, err := secrets.Resolve(vaultName, source, sourceName, secrets.OAuthClientSecret, nil)
	if err != nil {
		fmt.Fprint(out, "OAuth client secret: ")
		clientSecret, err = readLine(in)
		if err != nil || clientSecret == "" {
			return "", "", fmt.Errorf("an OAuth client secret is required: %w", err)
		}
		if serr := secrets.Store(vaultName, source, secrets.OAuthClientSecret, clientSecret); serr != nil {
			return "", "", fmt.Errorf("store client secret in the OS keyring: %w", serr)
		}
	}
	return clientID, clientSecret, nil
}

func buildAuthURL(authURL, clientID, redirectURI string) string {
	q := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {gmailScope},
		"access_type":   {"offline"}, // required for a refresh token
		"prompt":        {"consent"}, // force re-issue on re-auth
	}
	return authURL + "?" + q.Encode()
}

// waitForCode serves exactly one loopback request and extracts ?code=.
func waitForCode(ctx context.Context, ln net.Listener) (string, error) {
	type result struct {
		code string
		err  error
	}
	ch := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing ?code parameter", http.StatusBadRequest)
			ch <- result{err: fmt.Errorf("authorization failed: %s", r.URL.Query().Get("error"))}
			return
		}
		fmt.Fprintln(w, "pkms is authorized — you can close this tab.")
		ch <- result{code: code}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	select {
	case r := <-ch:
		return r.code, r.err
	case <-ctx.Done():
		return "", errors.New("timed out waiting for the OAuth redirect; re-run `pkms auth` and complete the browser flow within 5 minutes")
	}
}

// exchangeCode trades the authorization code for a refresh token.
func exchangeCode(ctx context.Context, tokenURL, clientID, clientSecret, code, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("exchange authorization code: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var tok struct {
		RefreshToken string `json:"refresh_token"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("exchange authorization code: HTTP %d with unparseable body", resp.StatusCode)
	}
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token in the response (%s %s); ensure access_type=offline was granted and try again", tok.Error, tok.ErrorDesc)
	}
	return tok.RefreshToken, nil
}

func readLine(in io.Reader) (string, error) {
	var line strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line.WriteByte(buf[0])
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
	}
	return strings.TrimSpace(line.String()), nil
}
