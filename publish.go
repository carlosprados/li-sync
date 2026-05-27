package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const defaultSiteBaseURL = "https://carlos.enredando.me"

// siteBaseURL returns the base URL used to build the article preview link.
// Overridable via LISYNC_BASE_URL so the tool can serve other Hugo sites
// without recompiling.
func siteBaseURL() string {
	if v := os.Getenv("LISYNC_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultSiteBaseURL
}

func runPublish(root string, args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return errors.New("usage: publish <slug> [--at <datetime>] [--force] [--dry-run]")
	}
	slug := args[0]

	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	atFlag := fs.String("at", "", "override the publish/schedule datetime (default: post's date)")
	forceFlag := fs.Bool("force", false, "publish even if slug already has an entry in linkedin-status.yaml")
	dryRunFlag := fs.Bool("dry-run", false, "print the payload that would be sent and exit (no API call)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

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
	if existing, exists := st.Posts[slug]; exists && !*forceFlag {
		return fmt.Errorf("%q already recorded as %s in %s — pass --force to republish", slug, existing.Status, stateFileName)
	}

	var publishAt time.Time
	if *atFlag != "" {
		publishAt, err = parseFlexibleTime(*atFlag)
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

	articleURL := fmt.Sprintf("%s/posts/%s/", siteBaseURL(), slug)

	payload := buildPostPayload(commentary, articleURL, target.Title, scheduled, publishAt)

	if *dryRunFlag {
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
