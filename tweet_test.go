package main

import (
	"strings"
	"testing"
)

func TestTweetLen(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"plain ascii", "hello world", 11},
		{"unicode runes count as one", "señal 𝗯𝗼𝗹𝗱", 10},
		{"url counts as 23", "https://example.com/a/very/long/path/that/never/ends", 23},
		{"url plus text", "see https://example.com/x now", 3 + 1 + 23 + 1 + 3},
		{"two urls", "https://a.com https://b.com/long/path", 23 + 1 + 23},
		{"newlines count", "a\nb\n\nc", 6},
		{"non-url scheme-ish token", "http://", 7}, // no host → not a URL
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tweetLen(tt.in); got != tt.want {
				t.Errorf("tweetLen(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildXIntentURL(t *testing.T) {
	u := buildXIntentURL("hola & adiós\nhttps://example.com/x")
	if !strings.HasPrefix(u, xIntentURL+"?") {
		t.Errorf("intent URL = %q, want prefix %q", u, xIntentURL+"?")
	}
	// The raw text must be query-encoded: no literal &, newline, or space survives.
	query := strings.TrimPrefix(u, xIntentURL+"?")
	for _, bad := range []string{" ", "\n", "& "} {
		if strings.Contains(query, bad) {
			t.Errorf("query %q contains unencoded %q", query, bad)
		}
	}
	if !strings.Contains(query, "text=") {
		t.Errorf("query %q missing text param", query)
	}
}

func TestTweetLenBoundary(t *testing.T) {
	exact := strings.Repeat("a", tweetMaxWeightedLen)
	if got := tweetLen(exact); got != tweetMaxWeightedLen {
		t.Errorf("tweetLen(280×a) = %d, want %d", got, tweetMaxWeightedLen)
	}
	over := exact + "b"
	if got := tweetLen(over); got != tweetMaxWeightedLen+1 {
		t.Errorf("tweetLen(281×a) = %d, want %d", got, tweetMaxWeightedLen+1)
	}
}
