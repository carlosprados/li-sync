package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// XPublishResult is the outcome of an X publish/republish, returned to the
// caller so presentation lives in the front-end. On a dry run, TweetID is
// empty and Text holds the tweet that would be posted.
type XPublishResult struct {
	Slug    string
	DryRun  bool
	TweetID string
	Text    string
	URL     string // article URL the card is scraped from
}

// runXPublish posts a post's x-post.txt companion to X. Unlike LinkedIn there
// is no scheduling: the X API cannot schedule tweets, so future-dated posts
// are refused — run the command on/after the post's date.
//   - force:    publish even if the slug already has an x-status.yaml entry
//   - dryRun:   run the checks and return the tweet text, no API call / no auth
//   - noVerify: skip the article preflight (not recommended)
//   - baseURL:  site base URL for the article link (resolved from Viper by the caller)
//   - rep:      receives progress steps
//
// It returns an XPublishResult; the caller renders it. No stdout writes happen here.
func runXPublish(root, slug string, force, dryRun, noVerify bool, baseURL string, rep Reporter) (XPublishResult, error) {
	posts, err := scanPosts(root)
	if err != nil {
		return XPublishResult{}, err
	}
	var target *post
	for i := range posts {
		if posts[i].Slug == slug {
			target = &posts[i]
			break
		}
	}
	if target == nil {
		return XPublishResult{}, fmt.Errorf("no post named %q under %s/", slug, contentPostsDir)
	}
	if target.Draft {
		return XPublishResult{}, fmt.Errorf("%q is marked as draft — publish refused", slug)
	}
	companion := xCompanionPath(root, slug)
	if !hasXCompanion(root, slug) {
		return XPublishResult{}, fmt.Errorf("%q has no %s", slug, xCompanionFile)
	}

	st, err := loadXState(root)
	if err != nil {
		return XPublishResult{}, err
	}
	if existing, exists := st.Posts[slug]; exists && !force {
		return XPublishResult{}, fmt.Errorf("%q already recorded as %s in %s — pass --force to republish", slug, existing.Status, xStateFileName)
	}

	now := time.Now()
	if target.Date.After(now) {
		return XPublishResult{}, fmt.Errorf("%q is dated %s, in the future — X cannot schedule tweets via the API; run this on/after the post's date", slug, formatDate(target.Date))
	}

	body, err := os.ReadFile(companion)
	if err != nil {
		return XPublishResult{}, fmt.Errorf("read companion: %w", err)
	}
	text := strings.TrimSpace(string(body))
	if text == "" {
		return XPublishResult{}, fmt.Errorf("%s is empty", companion)
	}
	if n := tweetLen(text); n > tweetMaxWeightedLen {
		return XPublishResult{}, fmt.Errorf("companion %s is ~%d weighted chars, over X's %d limit — trim it (URLs count as %d regardless of length)", companion, n, tweetMaxWeightedLen, tcoURLLen)
	}

	articleURL := fmt.Sprintf("%s/posts/%s/", baseURL, target.URLSlug)
	// Domain-agnostic check: companions may link through a short domain that
	// redirects to the canonical one; the card works either way.
	if !strings.Contains(text, "/posts/"+target.URLSlug+"/") {
		rep.Stepf("warning: %s does not link the article (no \"/posts/%s/\" URL) — without it X renders no link card", xCompanionFile, target.URLSlug)
	}

	// Preflight: X scrapes the link card (twitter:card / og: meta) from the
	// article page at tweet time. Refuse to tweet against a dead page.
	if !noVerify {
		og, verr := verifyArticleOG(articleURL)
		if verr != nil {
			return XPublishResult{}, fmt.Errorf("preflight failed: %w\n  → fix the page (or wait for deploy), then retry; pass --no-verify only if you know the page is fine", verr)
		}
		rep.Stepf("preflight OK: %s is live, og:image %s reachable", articleURL, og.Image)
	}

	if dryRun {
		return XPublishResult{Slug: slug, DryRun: true, Text: text, URL: articleURL}, nil
	}

	toks, err := loadXTokens()
	if err != nil {
		return XPublishResult{}, err
	}
	toks, err = ensureFreshXTokens(toks)
	if err != nil {
		return XPublishResult{}, err
	}

	tweetID, err := postTweet(toks.AccessToken, text)
	if err != nil {
		return XPublishResult{}, err
	}

	st.Posts[slug] = stateEntry{Status: "published", ScheduledFor: now, Note: tweetID}
	if err := saveXState(root, st); err != nil {
		return XPublishResult{}, fmt.Errorf("tweet posted (id %s) but writing %s failed: %w — record it manually with `li-sync x mark %s --note %s`", tweetID, xStateFileName, err, slug, tweetID)
	}

	return XPublishResult{Slug: slug, TweetID: tweetID, Text: text, URL: articleURL}, nil
}

// runXRepublish deletes the existing tweet and posts a fresh one from the
// current companion. X has no edit endpoint, so this is the only way to change
// a published tweet's text or refresh its card.
func runXRepublish(root, slug string, noVerify bool, baseURL string, rep Reporter) (XPublishResult, error) {
	st, err := loadXState(root)
	if err != nil {
		return XPublishResult{}, err
	}
	entry, ok := st.Posts[slug]
	if !ok || entry.Note == "" {
		return XPublishResult{}, fmt.Errorf("%q has no recorded tweet ID in %s — use `x publish` instead", slug, xStateFileName)
	}

	toks, err := loadXTokens()
	if err != nil {
		return XPublishResult{}, err
	}
	toks, err = ensureFreshXTokens(toks)
	if err != nil {
		return XPublishResult{}, err
	}

	if err := deleteTweet(toks.AccessToken, entry.Note); err != nil {
		return XPublishResult{}, fmt.Errorf("delete existing tweet %s: %w", entry.Note, err)
	}
	rep.Stepf("deleted old tweet %s — posting a fresh one...", entry.Note)

	// force=true overwrites the stale state entry with the new tweet ID.
	return runXPublish(root, slug, true, false, noVerify, baseURL, rep)
}
