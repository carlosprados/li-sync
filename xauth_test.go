package main

import (
	"strings"
	"testing"
)

// clearXAppCredEnv blanks the X credential env vars for the duration of a test
// so the resolution order is deterministic regardless of the host env.
func clearXAppCredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("X_CLIENT_ID", "")
	t.Setenv("X_CLIENT_SECRET", "")
}

func TestCodeVerifier(t *testing.T) {
	v, err := generateCodeVerifier()
	if err != nil {
		t.Fatalf("generateCodeVerifier: %v", err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Errorf("verifier length = %d, want 43–128 (RFC 7636)", len(v))
	}
	for _, r := range v {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", r) {
			t.Errorf("verifier contains non-base64url rune %q", r)
		}
	}
}

func TestCodeChallengeS256(t *testing.T) {
	// RFC 7636 Appendix B test vector.
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := codeChallengeS256(verifier); got != want {
		t.Errorf("codeChallengeS256 = %q, want %q", got, want)
	}
}

func TestBuildXAuthorizeURL(t *testing.T) {
	u := buildXAuthorizeURL("cid", "http://localhost:8765/callback", "st4te", "ch4llenge")
	for _, sub := range []string{
		"response_type=code",
		"code_challenge=ch4llenge",
		"code_challenge_method=S256",
		"tweet.write",
		"offline.access",
	} {
		if !strings.Contains(u, sub) {
			t.Errorf("authorize URL missing %q: %s", sub, u)
		}
	}
}

func TestResolveXAuthCredentialsFlags(t *testing.T) {
	clearXAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	creds, persist, err := resolveXAuthCredentials("flag-id", "flag-secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.ClientID != "flag-id" || creds.ClientSecret != "flag-secret" {
		t.Errorf("creds = %+v, want flag values", creds)
	}
	if !persist {
		t.Error("persist = false, want true for flag-provided credentials")
	}
}

func TestResolveXAuthCredentialsPublicClient(t *testing.T) {
	clearXAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	// A client ID alone is a valid X public (PKCE) client — no secret required.
	creds, persist, err := resolveXAuthCredentials("only-id", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.ClientID != "only-id" || creds.ClientSecret != "" {
		t.Errorf("creds = %+v, want public client with empty secret", creds)
	}
	if !persist {
		t.Error("persist = false, want true for flag-provided credentials")
	}
}

func TestResolveXAuthCredentialsSecretWithoutID(t *testing.T) {
	clearXAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	if _, _, err := resolveXAuthCredentials("", "only-secret"); err == nil {
		t.Error("expected error when only --client-secret is provided, got nil")
	}
}

func TestResolveXAuthCredentialsEnv(t *testing.T) {
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())
	t.Setenv("X_CLIENT_ID", "env-id")
	t.Setenv("X_CLIENT_SECRET", "env-secret")

	creds, persist, err := resolveXAuthCredentials("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.ClientID != "env-id" || creds.ClientSecret != "env-secret" {
		t.Errorf("creds = %+v, want env values", creds)
	}
	if persist {
		t.Error("persist = true, want false for env-provided credentials")
	}
}

func TestResolveXAuthCredentialsFile(t *testing.T) {
	clearXAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	want := appCreds{ClientID: "file-id"} // public client: empty secret is valid
	if err := saveXAppCredentials(want); err != nil {
		t.Fatalf("saveXAppCredentials: %v", err)
	}

	creds, persist, err := resolveXAuthCredentials("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds != want {
		t.Errorf("creds = %+v, want %+v", creds, want)
	}
	if persist {
		t.Error("persist = true, want false for credentials loaded from x_app.json")
	}
}
