package imap

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
)

// refreshAccessToken exchanges a stored refresh token for a short-lived
// access token (SPEC §24: refreshed every run, never persisted).
func refreshAccessToken(ctx context.Context, tokenURL, clientID, clientSecret, refreshToken string) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("refresh OAuth token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("refresh OAuth token: HTTP %d with unparseable body", resp.StatusCode)
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("refresh OAuth token failed: %s %s (a 7-day expiry means the OAuth consent screen is still in Testing mode — see docs/OAUTH-GMAIL.md; re-run `pkms auth` after fixing it)", tok.Error, tok.ErrorDesc)
	}
	return tok.AccessToken, nil
}

// xoauth2Client implements the XOAUTH2 SASL mechanism (SPEC §24) —
// go-sasl ships only OAUTHBEARER, and Gmail's documented IMAP mechanism
// is XOAUTH2.
type xoauth2Client struct {
	username, token string
	failed          bool
}

func newXOAUTH2Client(username, token string) sasl.Client {
	return &xoauth2Client{username: username, token: token}
}

func (x *xoauth2Client) Start() (string, []byte, error) {
	return "XOAUTH2", []byte(xoauth2String(x.username, x.token)), nil
}

// Next handles the failure path: the server sends a base64 JSON challenge
// and expects an empty response before it issues the tagged NO.
func (x *xoauth2Client) Next(challenge []byte) ([]byte, error) {
	if x.failed {
		return nil, fmt.Errorf("XOAUTH2 rejected: %s", challenge)
	}
	x.failed = true
	return []byte{}, nil
}
