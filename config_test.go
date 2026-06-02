package main

import "testing"

func TestLastRepoRoundTrip(t *testing.T) {
	t.Setenv("LI_SYNC_CONFIG_DIR", t.TempDir())

	if got := loadLastRepo(); got != "" {
		t.Errorf("loadLastRepo with no file = %q, want empty", got)
	}

	want := "/home/charlie/blog"
	if err := saveLastRepo(want); err != nil {
		t.Fatalf("saveLastRepo: %v", err)
	}
	if got := loadLastRepo(); got != want {
		t.Errorf("loadLastRepo = %q, want %q", got, want)
	}
}
