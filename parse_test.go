package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFlexibleTime(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
		check   func(time.Time) bool
	}{
		{"2026-05-20T08:30:00+02:00", false, func(tm time.Time) bool { return tm.Year() == 2026 && tm.Minute() == 30 }},
		{"2026-05-20T08:30:00", false, func(tm time.Time) bool { return tm.Hour() == 8 && tm.Minute() == 30 }},
		{"2026-05-20 09:15", false, func(tm time.Time) bool { return tm.Hour() == 9 && tm.Minute() == 15 }},
		{"2026-05-20", false, func(tm time.Time) bool { return tm.Year() == 2026 && tm.Month() == time.May && tm.Day() == 20 }},
		{"not-a-date", true, nil},
		{"", true, nil},
	}
	for _, c := range cases {
		got, err := parseFlexibleTime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseFlexibleTime(%q): expected error, got none", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseFlexibleTime(%q): unexpected error: %v", c.in, err)
			continue
		}
		if c.check != nil && !c.check(got) {
			t.Errorf("parseFlexibleTime(%q): value check failed, got %v", c.in, got)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestParseFrontMatterYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.md")
	writeFile(t, p, `---
title: "The Reflection Pattern"
description: "Producer-Critic loop."
date: 2026-06-02
draft: false
slug: "agentic-ai-reflection"
---

Body text here.
`)
	fm, err := parseFrontMatter(p)
	if err != nil {
		t.Fatalf("parseFrontMatter: %v", err)
	}
	if fm.Title != "The Reflection Pattern" {
		t.Errorf("Title = %q", fm.Title)
	}
	if fm.Slug != "agentic-ai-reflection" {
		t.Errorf("Slug = %q", fm.Slug)
	}
	if fm.Draft {
		t.Errorf("Draft = true, want false")
	}
	if fm.Date.Year() != 2026 || fm.Date.Month() != time.June || fm.Date.Day() != 2 {
		t.Errorf("Date = %v, want 2026-06-02", fm.Date)
	}
}

func TestParseFrontMatterTOML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "index.md")
	writeFile(t, p, `+++
title = "Routing"
draft = true
slug = "agentic-ai-routing"
+++

Body.
`)
	fm, err := parseFrontMatter(p)
	if err != nil {
		t.Fatalf("parseFrontMatter: %v", err)
	}
	if fm.Title != "Routing" {
		t.Errorf("Title = %q", fm.Title)
	}
	if !fm.Draft {
		t.Errorf("Draft = false, want true")
	}
}

func TestParseFrontMatterErrors(t *testing.T) {
	dir := t.TempDir()

	bad := filepath.Join(dir, "bad.md")
	writeFile(t, bad, "no front matter here\njust text\n")
	if _, err := parseFrontMatter(bad); err == nil {
		t.Error("expected error for missing delimiter, got nil")
	}

	unterminated := filepath.Join(dir, "unterminated.md")
	writeFile(t, unterminated, "---\ntitle: x\n")
	if _, err := parseFrontMatter(unterminated); err == nil {
		t.Error("expected error for unterminated front matter, got nil")
	}
}
