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
	xAuthorizeURL = "https://x.com/i/oauth2/authorize"
	xTokenURL     = "https://api.x.com/2/oauth2/token"
	xTweetsURL    = "https://api.x.com/2/tweets"
	xUsersMeURL   = "https://api.x.com/2/users/me"
	xOAuthScopes  = "tweet.read tweet.write users.read offline.access"
)

// xTokenRequest posts to X's token endpoint. Confidential clients (secret set)
// authenticate with HTTP Basic; public clients (PKCE, no secret) send only the
// client_id in the body — callers set it there.
func xTokenRequest(creds appCreds, v url.Values) (tokenResponse, error) {
	var tr tokenResponse
	req, err := http.NewRequest("POST", xTokenURL, strings.NewReader(v.Encode()))
	if err != nil {
		return tr, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if creds.ClientSecret != "" {
		req.SetBasicAuth(creds.ClientID, creds.ClientSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return tr, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tr, fmt.Errorf("X token endpoint %d: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return tr, fmt.Errorf("parse X token response: %w", err)
	}
	return tr, nil
}

func exchangeXCodeForTokens(creds appCreds, code, redirectURI, codeVerifier string) (tokenResponse, error) {
	v := url.Values{}
	v.Set("grant_type", "authorization_code")
	v.Set("code", code)
	v.Set("redirect_uri", redirectURI)
	v.Set("client_id", creds.ClientID)
	v.Set("code_verifier", codeVerifier)
	return xTokenRequest(creds, v)
}

func refreshXAccessToken(creds appCreds, refreshToken string) (tokenResponse, error) {
	v := url.Values{}
	v.Set("grant_type", "refresh_token")
	v.Set("refresh_token", refreshToken)
	v.Set("client_id", creds.ClientID)
	return xTokenRequest(creds, v)
}

type xUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

func fetchXUser(accessToken string) (xUser, error) {
	var u xUser
	req, _ := http.NewRequest("GET", xUsersMeURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return u, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return u, fmt.Errorf("X users/me %d: %s", resp.StatusCode, string(body))
	}
	var wrapper struct {
		Data xUser `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return u, err
	}
	return wrapper.Data, nil
}

// ensureFreshXTokens refreshes the access token if it expires within 5 minutes.
// X rotates refresh tokens on every use (single-use), so the new store MUST be
// persisted before it is returned — a persist failure is a hard error, never
// proceed with an un-persisted rotated token (the on-disk one is already dead).
func ensureFreshXTokens(t xTokenStore) (xTokenStore, error) {
	if time.Now().Add(5 * time.Minute).Before(t.ExpiresAt) {
		return t, nil
	}
	creds, err := loadXAppCredentials()
	if err != nil {
		return t, err
	}
	tr, err := refreshXAccessToken(creds, t.RefreshToken)
	if err != nil {
		return t, fmt.Errorf("X refresh failed: %w (run `li-sync x auth` to re-authenticate)", err)
	}
	t.AccessToken = tr.AccessToken
	t.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.RefreshToken != "" {
		t.RefreshToken = tr.RefreshToken
	}
	if err := saveXTokens(t); err != nil {
		return t, fmt.Errorf("X tokens refreshed but save failed: %w — aborting (the rotated refresh token would be lost)", err)
	}
	return t, nil
}

// postTweet creates a tweet and returns its ID. The card is scraped by X from
// the URL inside the text (twitter:card meta on the page) — no media upload.
func postTweet(accessToken, text string) (string, error) {
	data, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("POST", xTweetsURL, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("X API %d: %s", resp.StatusCode, string(body))
	}
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("parse create tweet response: %w", err)
	}
	if created.Data.ID == "" {
		return "", fmt.Errorf("create tweet returned no id: %s", string(body))
	}
	return created.Data.ID, nil
}

// deleteTweet deletes a tweet by ID. A 404 is treated as success (already
// gone), so republish is idempotent if the tweet was removed manually.
func deleteTweet(accessToken, id string) error {
	req, _ := http.NewRequest("DELETE", xTweetsURL+"/"+url.PathEscape(id), nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("X API %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
