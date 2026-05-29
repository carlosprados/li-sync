package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	linkedinAuthorizeURL = "https://www.linkedin.com/oauth/v2/authorization"
	linkedinTokenURL     = "https://www.linkedin.com/oauth/v2/accessToken"
	linkedinUserinfoURL  = "https://api.linkedin.com/v2/userinfo"
	linkedinPostsURL     = "https://api.linkedin.com/rest/posts"
	linkedinImagesURL    = "https://api.linkedin.com/rest/images"
	linkedinAPIVersion   = "202605"
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

// articleOG holds the Open Graph fields scraped from an article page.
type articleOG struct {
	Title string
	Image string
}

var metaTagRe = regexp.MustCompile(`(?is)<meta\b[^>]*>`)

// extractMetaContent finds the first <meta> tag whose property/name equals key
// and returns its content attribute. Attribute order is not assumed.
func extractMetaContent(html, key string) string {
	keyRe := regexp.MustCompile(`(?i)(?:property|name)\s*=\s*["']` + regexp.QuoteMeta(key) + `["']`)
	contentRe := regexp.MustCompile(`(?i)content\s*=\s*["']([^"']*)["']`)
	for _, tag := range metaTagRe.FindAllString(html, -1) {
		if keyRe.MatchString(tag) {
			if m := contentRe.FindStringSubmatch(tag); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// verifyArticleOG is the publish preflight. It guarantees the article page is
// live (HTTP 200) and exposes a reachable og:image before we let LinkedIn snapshot
// the link card. LinkedIn freezes the card at post-creation time and caches the OG
// per URL, so publishing against a 404 or a missing image bakes a broken card that
// no later fix can repair without deleting the post. Refusing here is what makes
// that failure impossible.
func verifyArticleOG(articleURL string) (articleOG, error) {
	var og articleOG
	client := &http.Client{Timeout: 15 * time.Second}

	req, _ := http.NewRequest("GET", articleURL, nil)
	req.Header.Set("User-Agent", "li-sync-preflight/1.0 (+LinkedIn card check)")
	resp, err := client.Do(req)
	if err != nil {
		return og, fmt.Errorf("could not fetch article page %s: %w", articleURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // <head> is well within 1MB
	if resp.StatusCode != http.StatusOK {
		return og, fmt.Errorf("article page %s returned HTTP %d — not deployed yet? "+
			"(for future-dated posts the page is dark until its date; publish on the day, "+
			"not weeks ahead, or LinkedIn caches an empty card)", articleURL, resp.StatusCode)
	}

	html := string(body)
	og.Title = extractMetaContent(html, "og:title")
	og.Image = extractMetaContent(html, "og:image")
	if og.Image == "" {
		return og, fmt.Errorf("article page %s exposes no og:image — LinkedIn would render an imageless card", articleURL)
	}
	if u, perr := url.Parse(og.Image); perr == nil && !u.IsAbs() {
		if base, berr := url.Parse(articleURL); berr == nil {
			og.Image = base.ResolveReference(u).String()
		}
	}

	ireq, _ := http.NewRequest("GET", og.Image, nil)
	ireq.Header.Set("User-Agent", "li-sync-preflight/1.0")
	ireq.Header.Set("Range", "bytes=0-0")
	iresp, err := client.Do(ireq)
	if err != nil {
		return og, fmt.Errorf("og:image %s is unreachable: %w", og.Image, err)
	}
	defer iresp.Body.Close()
	if iresp.StatusCode < 200 || iresp.StatusCode >= 300 {
		return og, fmt.Errorf("og:image %s returned HTTP %d — LinkedIn would render an imageless card", og.Image, iresp.StatusCode)
	}
	if ct := iresp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return og, fmt.Errorf("og:image %s has content-type %q, not an image", og.Image, ct)
	}
	return og, nil
}

// linkedinHeaders sets the auth + versioned REST headers common to every Posts
// API call.
func linkedinHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("LinkedIn-Version", linkedinAPIVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("Content-Type", "application/json")
}

// postToLinkedIn submits a Posts API payload and returns the created post URN
// (from the x-restli-id response header). Non-2xx responses surface the raw body.
func postToLinkedIn(accessToken string, payload map[string]any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("POST", linkedinPostsURL, bytes.NewReader(data))
	linkedinHeaders(req, accessToken)
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

// uploadImage uploads a local image to LinkedIn's Images API and returns its
// image URN (urn:li:image:...). This is REQUIRED for article cards to show a
// picture: the Posts API does not scrape the article URL's og:image, so the
// thumbnail must be an uploaded asset. Two steps: initializeUpload to get a
// pre-signed uploadUrl + image URN, then PUT the bytes.
func uploadImage(accessToken, ownerURN, imagePath string) (string, error) {
	initBody, err := json.Marshal(map[string]any{
		"initializeUploadRequest": map[string]any{"owner": ownerURN},
	})
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequest("POST", linkedinImagesURL+"?action=initializeUpload", bytes.NewReader(initBody))
	linkedinHeaders(req, accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("images initializeUpload %d: %s", resp.StatusCode, string(body))
	}
	var init struct {
		Value struct {
			UploadURL string `json:"uploadUrl"`
			Image     string `json:"image"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &init); err != nil {
		return "", fmt.Errorf("parse initializeUpload response: %w", err)
	}
	if init.Value.UploadURL == "" || init.Value.Image == "" {
		return "", fmt.Errorf("initializeUpload returned no uploadUrl/image: %s", string(body))
	}

	data, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("read image %s: %w", imagePath, err)
	}
	put, _ := http.NewRequest("PUT", init.Value.UploadURL, bytes.NewReader(data))
	put.Header.Set("Authorization", "Bearer "+accessToken)
	putResp, err := http.DefaultClient.Do(put)
	if err != nil {
		return "", fmt.Errorf("upload image bytes: %w", err)
	}
	defer putResp.Body.Close()
	putBody, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return "", fmt.Errorf("upload image bytes %d: %s", putResp.StatusCode, string(putBody))
	}
	return init.Value.Image, nil
}

// editLinkedInPostCommentary edits the text (commentary) of an existing post via
// a PARTIAL_UPDATE. Note: the LinkedIn Posts API only allows editing the
// commentary — the article card / media of a published post cannot be changed.
// To replace the card (e.g. after fixing an Open Graph image) you must delete
// and re-create the post; see `republish`.
// encodeURN escapes a URN for use as a REST path segment. LinkedIn requires the
// colons percent-encoded (urn:li:share:1 → urn%3Ali%3Ashare%3A1); url.PathEscape
// leaves ':' untouched, so use QueryEscape.
func encodeURN(urn string) string { return url.QueryEscape(urn) }

func editLinkedInPostCommentary(accessToken, postURN, commentary string) error {
	endpoint := linkedinPostsURL + "/" + encodeURN(postURN)
	patch := map[string]any{
		"patch": map[string]any{
			"$set": map[string]any{"commentary": commentary},
		},
	}
	data, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	req, _ := http.NewRequest("POST", endpoint, bytes.NewReader(data))
	linkedinHeaders(req, accessToken)
	req.Header.Set("X-RestLi-Method", "PARTIAL_UPDATE")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("LinkedIn API %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// deleteLinkedInPost deletes an existing post by URN. A 404 is treated as
// success (already gone), so republish is idempotent if the post was removed
// manually.
func deleteLinkedInPost(accessToken, postURN string) error {
	endpoint := linkedinPostsURL + "/" + encodeURN(postURN)
	req, _ := http.NewRequest("DELETE", endpoint, nil)
	linkedinHeaders(req, accessToken)
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
		return fmt.Errorf("LinkedIn API %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
