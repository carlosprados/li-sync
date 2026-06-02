package main

import "testing"

// clearAppCredEnv blanks the LinkedIn credential env vars for the duration of a
// test so the resolution order is deterministic regardless of the host env.
func clearAppCredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LINKEDIN_CLIENT_ID", "")
	t.Setenv("LINKEDIN_CLIENT_SECRET", "")
}

func TestResolveAuthCredentialsFlags(t *testing.T) {
	clearAppCredEnv(t)
	// Point the config dir at an empty temp dir so no stray app.json interferes.
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	creds, persist, err := resolveAuthCredentials("flag-id", "flag-secret")
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

func TestResolveAuthCredentialsMismatchedFlags(t *testing.T) {
	clearAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	if _, _, err := resolveAuthCredentials("only-id", ""); err == nil {
		t.Error("expected error when only --client-id is provided, got nil")
	}
}

func TestResolveAuthCredentialsEnv(t *testing.T) {
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())
	t.Setenv("LINKEDIN_CLIENT_ID", "env-id")
	t.Setenv("LINKEDIN_CLIENT_SECRET", "env-secret")

	creds, persist, err := resolveAuthCredentials("", "")
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

func TestResolveAuthCredentialsFile(t *testing.T) {
	clearAppCredEnv(t)
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	want := appCreds{ClientID: "file-id", ClientSecret: "file-secret"}
	if err := saveAppCredentials(want); err != nil {
		t.Fatalf("saveAppCredentials: %v", err)
	}

	creds, persist, err := resolveAuthCredentials("", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds != want {
		t.Errorf("creds = %+v, want %+v", creds, want)
	}
	if persist {
		t.Error("persist = true, want false for credentials loaded from app.json")
	}
}
