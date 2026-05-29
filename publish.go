package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
)

const defaultSiteBaseURL = "https://carlos.enredando.me"

// siteBaseURL returns the base URL used to build the article preview link.
// Resolved by Viper: --base-url flag / LISYNC_BASE_URL env / config file /
// the built-in default, so the tool can serve other Hugo sites without a rebuild.
func siteBaseURL() string {
	if v := strings.TrimRight(viper.GetString("base_url"), "/"); v != "" {
		return v
	}
	return defaultSiteBaseURL
}

// runPublish publishes (or schedules) a post's companion to LinkedIn.
//   - at:       override datetime ("" → use the post's front-matter date)
//   - force:    publish even if the slug already has a state entry
//   - dryRun:   run the preflight and print the payload, no API call / no auth
//   - noVerify: skip the preflight (not recommended)
func runPublish(root, slug, at string, force, dryRun, noVerify bool) error {
	posts, err := scanPosts(root)
	if err != nil {
		return err
	}
	var target *post
	for i := range posts {
		if posts[i].Slug == slug {
			target = &posts[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no post named %q under %s/", slug, contentPostsDir)
	}
	if target.Draft {
		return fmt.Errorf("%q is marked as draft — publish refused", slug)
	}
	if !target.HasCompanion {
		return fmt.Errorf("%q has no %s", slug, companionFile)
	}

	st, err := loadState(root)
	if err != nil {
		return err
	}
	if existing, exists := st.Posts[slug]; exists && !force {
		return fmt.Errorf("%q already recorded as %s in %s — pass --force to republish", slug, existing.Status, stateFileName)
	}

	var publishAt time.Time
	if at != "" {
		publishAt, err = parseFlexibleTime(at)
		if err != nil {
			return fmt.Errorf("--at: %w", err)
		}
	} else {
		publishAt = target.Date
	}

	now := time.Now()
	scheduled := publishAt.After(now)
	if !scheduled && !publishAt.IsZero() && publishAt.Before(now) {
		fmt.Fprintf(os.Stderr, "warning: %s is in the past — publishing immediately instead of scheduling\n", formatDateTime(publishAt))
	}

	body, err := os.ReadFile(target.CompanionPath)
	if err != nil {
		return fmt.Errorf("read companion: %w", err)
	}
	commentary := strings.TrimSpace(string(body))
	if commentary == "" {
		return fmt.Errorf("%s is empty", target.CompanionPath)
	}

	articleURL := fmt.Sprintf("%s/posts/%s/", siteBaseURL(), target.URLSlug)

	// Preflight: never let LinkedIn snapshot a card from a dead page or a missing
	// image. This is the gate that prevents the "blank card, frozen forever" failure.
	if !noVerify {
		og, verr := verifyArticleOG(articleURL)
		if verr != nil {
			return fmt.Errorf("preflight failed: %w\n  → fix the page (or wait for deploy), then retry; pass --no-verify only if you know LinkedIn already has a good cache for this URL", verr)
		}
		fmt.Fprintf(os.Stderr, "preflight OK: %s is live, og:image %s reachable\n", articleURL, og.Image)
	}

	payload := buildPostPayload(commentary, articleURL, target.Title, scheduled, publishAt)

	if dryRun {
		// Author is filled in for real runs only; for dry-run we surface the
		// placeholder so the user can confirm the structure without auth.
		payload["author"] = "<urn:li:person:...>  (populated from tokens.json on real run)"
		encoded, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println("--- payload (dry run) ---")
		fmt.Println(string(encoded))
		fmt.Println("--- end payload ---")
		if scheduled {
			fmt.Printf("would schedule for %s\n", formatDateTime(publishAt))
		} else {
			fmt.Println("would publish immediately")
		}
		return nil
	}

	toks, err := loadTokens()
	if err != nil {
		return err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return err
	}
	payload["author"] = toks.PersonURN

	postURN, err := postToLinkedIn(toks.AccessToken, payload)
	if err != nil {
		return err
	}

	entry := stateEntry{Note: postURN}
	if scheduled {
		entry.Status = "scheduled"
		entry.ScheduledFor = publishAt
	} else {
		entry.Status = "published"
		entry.ScheduledFor = now
	}
	st.Posts[slug] = entry
	if err := saveState(root, st); err != nil {
		return fmt.Errorf("post published (URN %s) but writing %s failed: %w", postURN, stateFileName, err)
	}

	if scheduled {
		fmt.Printf("scheduled %s for %s (URN: %s)\n", slug, formatDateTime(publishAt), postURN)
	} else {
		fmt.Printf("published %s (URN: %s)\n", slug, postURN)
	}
	return nil
}

// runEdit updates the commentary (text) of an already-published post from its
// current linkedin-post.txt. The article card/media cannot be changed this way —
// use `republish` for that.
func runEdit(root, slug string) error {
	posts, err := scanPosts(root)
	if err != nil {
		return err
	}
	var target *post
	for i := range posts {
		if posts[i].Slug == slug {
			target = &posts[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no post named %q under %s/", slug, contentPostsDir)
	}
	if !target.HasCompanion {
		return fmt.Errorf("%q has no %s", slug, companionFile)
	}

	st, err := loadState(root)
	if err != nil {
		return err
	}
	entry, ok := st.Posts[slug]
	if !ok || entry.Note == "" {
		return fmt.Errorf("%q has no recorded LinkedIn URN in %s — publish it first", slug, stateFileName)
	}

	body, err := os.ReadFile(target.CompanionPath)
	if err != nil {
		return fmt.Errorf("read companion: %w", err)
	}
	commentary := strings.TrimSpace(string(body))
	if commentary == "" {
		return fmt.Errorf("%s is empty", target.CompanionPath)
	}

	toks, err := loadTokens()
	if err != nil {
		return err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return err
	}

	if err := editLinkedInPostCommentary(toks.AccessToken, entry.Note, commentary); err != nil {
		return err
	}
	fmt.Printf("edited %s commentary (URN: %s)\n", slug, entry.Note)
	return nil
}

// runRepublish deletes the existing LinkedIn post and creates a fresh one. This
// is the only way to change a published post's article card (e.g. after fixing
// the page's Open Graph image): editing commentary in place cannot. The new
// post runs the full preflight and gets a new URN recorded in the state file.
func runRepublish(root, slug, at string, noVerify bool) error {
	st, err := loadState(root)
	if err != nil {
		return err
	}
	entry, ok := st.Posts[slug]
	if !ok || entry.Note == "" {
		return fmt.Errorf("%q has no recorded LinkedIn URN in %s — use `publish` instead", slug, stateFileName)
	}

	toks, err := loadTokens()
	if err != nil {
		return err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return err
	}

	if err := deleteLinkedInPost(toks.AccessToken, entry.Note); err != nil {
		return fmt.Errorf("delete existing post %s: %w", entry.Note, err)
	}
	fmt.Fprintf(os.Stderr, "deleted old post %s — creating a fresh one...\n", entry.Note)

	// force=true overwrites the stale state entry with the new URN.
	return runPublish(root, slug, at, true, false, noVerify)
}

func buildPostPayload(commentary, articleURL, title string, scheduled bool, publishAt time.Time) map[string]any {
	payload := map[string]any{
		"commentary": commentary,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []string{},
			"thirdPartyDistributionChannels": []string{},
		},
		"content": map[string]any{
			"article": map[string]any{
				"source": articleURL,
				"title":  title,
			},
		},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	if scheduled {
		payload["publishedAt"] = publishAt.UnixMilli()
	}
	return payload
}
