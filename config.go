package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type appCreds struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type tokenStore struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	PersonURN    string    `json:"person_urn"`
}

// xTokenStore holds the X (Twitter) OAuth 2.0 tokens. X has no URN concept;
// the numeric user ID and @username identify the authenticated account.
// X refresh tokens are single-use (rotated on every refresh), so this file is
// rewritten by ensureFreshXTokens on each rotation.
type xTokenStore struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	UserID       string    `json:"user_id"`
	Username     string    `json:"username"`
}

func configDir() (string, error) {
	if d := os.Getenv("LI_SYNC_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "li-sync"), nil
}

func appCredsPath() string {
	d, _ := configDir()
	return filepath.Join(d, "app.json")
}

func tokensPath() string {
	d, _ := configDir()
	return filepath.Join(d, "tokens.json")
}

func xAppCredsPath() string {
	d, _ := configDir()
	return filepath.Join(d, "x_app.json")
}

func xTokensPath() string {
	d, _ := configDir()
	return filepath.Join(d, "x_tokens.json")
}

// writeConfigJSON marshals v and writes it to a 0600 file in the config dir,
// creating the dir if needed. Shared by every credential/token save below.
func writeConfigJSON(path string, v any) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func saveAppCredentials(c appCreds) error {
	return writeConfigJSON(appCredsPath(), c)
}

func saveXAppCredentials(c appCreds) error {
	return writeConfigJSON(xAppCredsPath(), c)
}

// loadXAppCredentialsFromFile reads x_app.json. Unlike the LinkedIn variant, an
// empty client_secret is valid: X public clients (PKCE) have no secret.
func loadXAppCredentialsFromFile() (appCreds, bool, error) {
	var c appCreds
	path := xAppCredsPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.ClientID == "" {
		return c, false, fmt.Errorf("%s missing client_id", path)
	}
	return c, true, nil
}

// loadXAppCredentials resolves the X app credentials non-interactively:
// env vars > x_app.json. Used by token refresh, where prompting is not an option.
func loadXAppCredentials() (appCreds, error) {
	var c appCreds
	c.ClientID = os.Getenv("X_CLIENT_ID")
	c.ClientSecret = os.Getenv("X_CLIENT_SECRET")
	if c.ClientID != "" {
		return c, nil
	}

	c, ok, err := loadXAppCredentialsFromFile()
	if err != nil {
		return c, err
	}
	if ok {
		return c, nil
	}
	return c, fmt.Errorf("no X app credentials found.\n  - pass --client-id (and --client-secret for a confidential client) to `li-sync x auth`, or\n  - set X_CLIENT_ID (+ X_CLIENT_SECRET) as env vars, or\n  - create %s with {\"client_id\": \"...\", \"client_secret\": \"...\"} (chmod 0600; secret may be empty for a public client)", xAppCredsPath())
}

func loadAppCredentialsFromFile() (appCreds, bool, error) {
	var c appCreds
	path := appCredsPath()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return c, false, fmt.Errorf("%s missing client_id or client_secret", path)
	}
	return c, true, nil
}

func loadAppCredentials() (appCreds, error) {
	var c appCreds
	c.ClientID = os.Getenv("LINKEDIN_CLIENT_ID")
	c.ClientSecret = os.Getenv("LINKEDIN_CLIENT_SECRET")
	if c.ClientID != "" && c.ClientSecret != "" {
		return c, nil
	}

	c, ok, err := loadAppCredentialsFromFile()
	if err != nil {
		return c, err
	}
	if ok {
		return c, nil
	}
	return c, fmt.Errorf("no LinkedIn app credentials found.\n  - pass --client-id and --client-secret to `li-sync auth` (saves them for next time), or\n  - set LINKEDIN_CLIENT_ID + LINKEDIN_CLIENT_SECRET as env vars, or\n  - create %s with {\"client_id\": \"...\", \"client_secret\": \"...\"} (chmod 0600)", appCredsPath())
}

func saveTokens(t tokenStore) error {
	return writeConfigJSON(tokensPath(), t)
}

func saveXTokens(t xTokenStore) error {
	return writeConfigJSON(xTokensPath(), t)
}

// lastRepoPath is a tiny single-line file in the config dir recording the last
// Hugo repo opened in the TUI. It is kept separate from config.yaml on purpose:
// it is tool-managed state (like tokens.json), and writing it must never clobber
// the user's hand-written config. It is consulted ONLY by the `tui` command — the
// CLI subcommands' repo resolution (resolveRepoRoot) is deliberately unaffected,
// so scripted/automated use has no hidden default.
func lastRepoPath() string {
	d, _ := configDir()
	return filepath.Join(d, "last_repo")
}

func loadLastRepo() string {
	data, err := os.ReadFile(lastRepoPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveLastRepo(path string) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return os.WriteFile(lastRepoPath(), []byte(path+"\n"), 0o644)
}

func loadTokens() (tokenStore, error) {
	var t tokenStore
	data, err := os.ReadFile(tokensPath())
	if errors.Is(err, os.ErrNotExist) {
		return t, errors.New("not authenticated. run `li-sync auth` first")
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse tokens: %w", err)
	}
	return t, nil
}

func loadXTokens() (xTokenStore, error) {
	var t xTokenStore
	data, err := os.ReadFile(xTokensPath())
	if errors.Is(err, os.ErrNotExist) {
		return t, errors.New("not authenticated with X. run `li-sync x auth` first")
	}
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("parse X tokens: %w", err)
	}
	return t, nil
}
