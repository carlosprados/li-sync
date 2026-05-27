package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	linkedinAuthorizeURL = "https://www.linkedin.com/oauth/v2/authorization"
	linkedinTokenURL     = "https://www.linkedin.com/oauth/v2/accessToken"
	linkedinUserinfoURL  = "https://api.linkedin.com/v2/userinfo"
	linkedinPostsURL     = "https://api.linkedin.com/rest/posts"
	linkedinAPIVersion   = "202405"
	oauthScopes          = "openid profile w_member_social email"
)

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshToken          string `json:"refresh_token"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	Scope                 string `json:"scope"`
	TokenType             string `json:"token_type"`
}

func tokenRequest(v url.Values) (tokenResponse, error) {
	var tr tokenResponse
	resp, err := http.Post(linkedinTokenURL, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("token endpoint %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return tr, fmt.Errorf("parse token response: %w", err)
	}
	return tr, nil
}

func exchangeCodeForTokens(creds appCreds, code, redirectURI string) (tokenResponse, error) {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", redirectURI)
	v.Set("client_id", creds.ClientID)
	v.Set("client_secret", creds.ClientSecret)
	return tokenRequest(v)
}

func refreshAccessToken(creds appCreds, refreshToken string) (tokenResponse, error) {
	v := url.Values{}
	v.Set("grant_type", "refresh_token")
	v.Set("refresh_token", refreshToken)
	v.Set("client_id", creds.ClientID)
	v.Set("client_secret", creds.ClientSecret)
	return tokenRequest(v)
}

type userinfoResponse struct {
	Sub   string `json:"sub"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func fetchUserSubject(accessToken string) (userinfoResponse, error) {
	var u userinfoResponse
	req, _ := http.NewRequest("GET", linkedinUserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("userinfo %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return u, err
	}
	return u, nil
}

// ensureFreshTokens refreshes the access token if it expires within 5 minutes.
// The (possibly updated) tokens are persisted before being returned.
func ensureFreshTokens(t tokenStore) (tokenStore, error) {
	if time.Now().Add(5 * time.Minute).Before(t.ExpiresAt) {
		return t, nil
	}
	creds, err := loadAppCredentials()
	if err != nil {
		return t, err
	}
	tr, err := refreshAccessToken(creds, t.RefreshToken)
	if err != nil {
		return t, fmt.Errorf("refresh failed: %w (run `li-sync auth` to re-authenticate)", err)
	}
	t.AccessToken = tr.AccessToken
	t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.RefreshToken != "" {
		t.RefreshToken = tr.RefreshToken
	}
	if err := saveTokens(t); err != nil {
		return t, err
	}
	return t, nil
}

// postToLinkedIn submits a Posts API payload and returns the created post URN
// (from the x-restli-id response header). Non-2xx responses surface the raw body.
func postToLinkedIn(accessToken string, payload map[string]any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("POST", linkedinPostsURL, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("LinkedIn-Version", linkedinAPIVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("LinkedIn API %d: %s", resp.StatusCode, string(body))
	}
	urn := resp.Header.Get("x-restli-id")
	if urn == "" {
		urn = resp.Header.Get("X-Restli-Id")
	}
	return urn, nil
}
