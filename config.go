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

func saveAppCredentials(c appCreds) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(appCredsPath(), data, 0o600)
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
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(tokensPath(), data, 0o600)
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
