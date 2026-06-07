package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// runXAuthFlow runs the one-time X OAuth 2.0 flow (Authorization Code + PKCE).
// cliID/cliSecret come from `x auth` flags; empty values fall through to
// resolveXAuthCredentials' env/file/prompt precedence. The secret is optional:
// X public clients (app type "Native App") authenticate with PKCE alone, while
// confidential clients ("Web App") also send Basic auth at the token endpoint.
func runXAuthFlow(cliID, cliSecret string) error {
	creds, persist, err := resolveXAuthCredentials(cliID, cliSecret)
	if err != nil {
		return err
	}
	if persist {
		if err := saveXAppCredentials(creds); err != nil {
			return fmt.Errorf("credentials resolved but save failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "credentials saved to %s\n", xAppCredsPath())
	}

	if !portAvailable(callbackPort) {
		return fmt.Errorf("port %d is in use — close whatever is listening on it and retry", callbackPort)
	}

	state, err := randomState()
	if err != nil {
		return err
	}
	verifier, err := generateCodeVerifier()
	if err != nil {
		return err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath)
	authURL := buildXAuthorizeURL(creds.ClientID, redirectURI, state, codeChallengeS256(verifier))

	fmt.Fprintln(os.Stderr, "opening X authorization in your browser...")
	fmt.Fprintln(os.Stderr, "if it does not open, visit this URL manually:")
	fmt.Fprintln(os.Stderr, "  "+authURL)

	if err := openURL(authURL); err != nil {
		fmt.Fprintf(os.Stderr, "(could not auto-open browser: %v)\n", err)
	}

	code, err := waitForCallback(state)
	if err != nil {
		return err
	}

	tr, err := exchangeXCodeForTokens(creds, code, redirectURI, verifier)
	if err != nil {
		return err
	}

	user, err := fetchXUser(tr.AccessToken)
	if err != nil {
		return fmt.Errorf("fetched tokens but users/me failed: %w", err)
	}

	store := xTokenStore{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		UserID:       user.ID,
		Username:     user.Username,
	}
	if err := saveXTokens(store); err != nil {
		return fmt.Errorf("tokens fetched but save failed: %w", err)
	}

	fmt.Printf("authenticated as %s (@%s)\n", user.Name, user.Username)
	fmt.Printf("tokens saved to %s\n", xTokensPath())
	fmt.Printf("access token valid until %s\n", store.ExpiresAt.Format("2006-01-02 15:04 MST"))
	if store.RefreshToken == "" {
		fmt.Fprintln(os.Stderr, "warning: no refresh token returned — check that the offline.access scope is enabled; you will need to re-auth every ~2 hours")
	}
	return nil
}

// generateCodeVerifier returns a PKCE code verifier (RFC 7636): 32 random
// bytes, base64url-encoded without padding → 43 chars, within the 43–128 range.
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallengeS256 derives the S256 code challenge from a verifier.
func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func buildXAuthorizeURL(clientID, redirectURI, state, challenge string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("scope", xOAuthScopes)
	v.Set("state", state)
	v.Set("code_challenge", challenge)
	v.Set("code_challenge_method", "S256")
	return xAuthorizeURL + "?" + v.Encode()
}

// resolveXAuthCredentials picks the X app credentials following the same
// precedence as LinkedIn (flags > env > file > interactive prompt) and reports
// whether the caller should persist them. Unlike LinkedIn, the client secret
// is optional everywhere: a client ID alone is a valid public (PKCE) client.
func resolveXAuthCredentials(cliID, cliSecret string) (appCreds, bool, error) {
	if cliID != "" {
		return appCreds{ClientID: cliID, ClientSecret: cliSecret}, true, nil
	}
	if cliSecret != "" {
		return appCreds{}, false, errors.New("--client-secret without --client-id makes no sense — pass --client-id too")
	}

	if id := os.Getenv("X_CLIENT_ID"); id != "" {
		return appCreds{ClientID: id, ClientSecret: os.Getenv("X_CLIENT_SECRET")}, false, nil
	}

	if c, ok, err := loadXAppCredentialsFromFile(); err != nil {
		return appCreds{}, false, err
	} else if ok {
		return c, false, nil
	}

	c, err := promptForXCredentials()
	if err != nil {
		return appCreds{}, false, err
	}
	return c, true, nil
}

func promptForXCredentials() (appCreds, error) {
	var c appCreds
	fmt.Fprintln(os.Stderr, "no X app credentials found in flags, env, or config file.")
	fmt.Fprintln(os.Stderr, "find them at https://developer.x.com/en/portal/projects-and-apps → your app → Keys and tokens → OAuth 2.0 Client ID and Client Secret.")
	fmt.Fprintln(os.Stderr)

	id, err := promptLine("Client ID: ")
	if err != nil {
		return c, err
	}
	c.ClientID = strings.TrimSpace(id)
	if c.ClientID == "" {
		return c, errors.New("Client ID cannot be empty")
	}

	secret, err := promptSecret("Client Secret (hidden; leave empty for a public client): ")
	if err != nil {
		return c, err
	}
	c.ClientSecret = strings.TrimSpace(secret)
	return c, nil
}
