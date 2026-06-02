package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// linkedinCommentaryMaxUTF16 is LinkedIn's hard limit on post commentary length.
// LinkedIn counts in UTF-16 code units, so SMP glyphs (the 𝗯𝗼𝗹𝗱 unicode used in
// companions, emoji) count as 2 each — `wc -m` undercounts them.
const linkedinCommentaryMaxUTF16 = 3000

// commentaryUTF16Len returns the length LinkedIn measures (UTF-16 code units).
func commentaryUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

var mentionTokenRe = regexp.MustCompile(`\{\{@([^}]+)\}\}`)

// applyMentions expands {{@Display Name}} tokens in the commentary into LinkedIn
// mention syntax @[Display Name](urn), looking the name up in the mentions map
// (config example: mentions: {"Amplía Soluciones": "urn:li:organization:123"}).
// Lookup keys must be lowercased (Viper's GetStringMapString already does this);
// matching is therefore case-insensitive. Unknown names are left as plain text
// with a warning, so a typo never ships a literal {{@...}} token.
func applyMentions(commentary string, mentions map[string]string) string {
	return mentionTokenRe.ReplaceAllStringFunc(commentary, func(tok string) string {
		name := mentionTokenRe.FindStringSubmatch(tok)[1]
		if urn, ok := mentions[strings.ToLower(strings.TrimSpace(name))]; ok && urn != "" {
			return fmt.Sprintf("@[%s](%s)", strings.TrimSpace(name), urn)
		}
		fmt.Fprintf(os.Stderr, "warning: no URN configured for mention %q (set it under \"mentions\" in the config file) — left as plain text\n", strings.TrimSpace(name))
		return strings.TrimSpace(name)
	})
}

// PublishResult is the outcome of a publish/republish, returned to the caller
// so presentation lives in the front-end (CLI prints it; a TUI renders it),
// not in the core logic. On a dry run, Payload holds the JSON that would be
// sent and URN is empty.
type PublishResult struct {
	Slug         string
	DryRun       bool
	Scheduled    bool
	PublishAt    time.Time
	URN          string
	Payload      map[string]any // populated only on a dry run
	FeaturedPath string         // bundle's featured image, "" if none
}

// runPublish publishes (or schedules) a post's companion to LinkedIn.
//   - at:       override datetime ("" → use the post's front-matter date)
//   - force:    publish even if the slug already has a state entry
//   - dryRun:   run the preflight and return the payload, no API call / no auth
//   - noVerify: skip the preflight (not recommended)
//   - baseURL:  site base URL for the article link (resolved from Viper by the caller)
//   - mentions: {{@Name}} → URN map (lowercased keys; resolved by the caller)
//   - rep:      receives progress steps (preflight, upload, schedule warnings)
//
// It returns a PublishResult; the caller renders it. No stdout writes happen here.
func runPublish(root, slug, at string, force, dryRun, noVerify bool, baseURL string, mentions map[string]string, rep Reporter) (PublishResult, error) {
	posts, err := scanPosts(root)
	if err != nil {
		return PublishResult{}, err
	}
	var target *post
	for i := range posts {
		if posts[i].Slug == slug {
			target = &posts[i]
			break
		}
	}
	if target == nil {
		return PublishResult{}, fmt.Errorf("no post named %q under %s/", slug, contentPostsDir)
	}
	if target.Draft {
		return PublishResult{}, fmt.Errorf("%q is marked as draft — publish refused", slug)
	}
	if !target.HasCompanion {
		return PublishResult{}, fmt.Errorf("%q has no %s", slug, companionFile)
	}

	st, err := loadState(root)
	if err != nil {
		return PublishResult{}, err
	}
	if existing, exists := st.Posts[slug]; exists && !force {
		return PublishResult{}, fmt.Errorf("%q already recorded as %s in %s — pass --force to republish", slug, existing.Status, stateFileName)
	}

	var publishAt time.Time
	if at != "" {
		publishAt, err = parseFlexibleTime(at)
		if err != nil {
			return PublishResult{}, fmt.Errorf("--at: %w", err)
		}
	} else {
		publishAt = target.Date
	}

	now := time.Now()
	scheduled := publishAt.After(now)
	if !scheduled && !publishAt.IsZero() && publishAt.Before(now) {
		rep.Stepf("warning: %s is in the past — publishing immediately instead of scheduling", formatDateTime(publishAt))
	}

	body, err := os.ReadFile(target.CompanionPath)
	if err != nil {
		return PublishResult{}, fmt.Errorf("read companion: %w", err)
	}
	commentary := strings.TrimSpace(string(body))
	if commentary == "" {
		return PublishResult{}, fmt.Errorf("%s is empty", target.CompanionPath)
	}
	commentary = applyMentions(commentary, mentions)
	if n := commentaryUTF16Len(commentary); n > linkedinCommentaryMaxUTF16 {
		return PublishResult{}, fmt.Errorf("companion %s is %d UTF-16 units, over LinkedIn's %d limit — trim it (LinkedIn counts each bold/emoji glyph as 2, so `wc -m` undercounts)", target.CompanionPath, n, linkedinCommentaryMaxUTF16)
	}

	articleURL := fmt.Sprintf("%s/posts/%s/", baseURL, target.URLSlug)

	// Preflight: never let LinkedIn snapshot a card from a dead page or a missing
	// image. This is the gate that prevents the "blank card, frozen forever" failure.
	if !noVerify {
		og, verr := verifyArticleOG(articleURL)
		if verr != nil {
			return PublishResult{}, fmt.Errorf("preflight failed: %w\n  → fix the page (or wait for deploy), then retry; pass --no-verify only if you know LinkedIn already has a good cache for this URL", verr)
		}
		rep.Stepf("preflight OK: %s is live, og:image %s reachable", articleURL, og.Image)
	}

	if dryRun {
		payload := buildPostPayload(commentary, articleURL, target.Title, target.Description, "", scheduled, publishAt)
		payload["author"] = "<urn:li:person:...>  (populated from tokens.json on real run)"
		if target.FeaturedPath != "" {
			payload["content"].(map[string]any)["article"].(map[string]any)["thumbnail"] = "<urn:li:image:...>  (uploaded from " + target.FeaturedPath + " on real run)"
		} else {
			rep.Stepf("warning: no featured image in the bundle — the article card would have NO picture")
		}
		return PublishResult{
			Slug: slug, DryRun: true, Scheduled: scheduled, PublishAt: publishAt,
			Payload: payload, FeaturedPath: target.FeaturedPath,
		}, nil
	}

	toks, err := loadTokens()
	if err != nil {
		return PublishResult{}, err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return PublishResult{}, err
	}

	// Upload the article thumbnail. Required for the card to show an image —
	// the Posts API does not scrape og:image.
	var thumbnailURN string
	if target.FeaturedPath != "" {
		rep.Stepf("uploading article thumbnail from %s...", target.FeaturedPath)
		thumbnailURN, err = uploadImage(toks.AccessToken, toks.PersonURN, target.FeaturedPath)
		if err != nil {
			return PublishResult{}, fmt.Errorf("upload thumbnail: %w", err)
		}
		rep.Stepf("thumbnail uploaded: %s", thumbnailURN)
	} else {
		rep.Stepf("warning: no featured image in the bundle — the article card will have NO picture")
	}

	payload := buildPostPayload(commentary, articleURL, target.Title, target.Description, thumbnailURN, scheduled, publishAt)
	payload["author"] = toks.PersonURN

	postURN, err := postToLinkedIn(toks.AccessToken, payload)
	if err != nil {
		return PublishResult{}, err
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
		return PublishResult{}, fmt.Errorf("post published (URN %s) but writing %s failed: %w", postURN, stateFileName, err)
	}

	return PublishResult{
		Slug: slug, Scheduled: scheduled, PublishAt: publishAt,
		URN: postURN, FeaturedPath: target.FeaturedPath,
	}, nil
}

// runEdit updates the commentary (text) of an already-published post from its
// current linkedin-post.txt and returns the post URN. The article card/media
// cannot be changed this way — use `republish` for that. The caller renders the
// result (CLI prints it; the TUI shows it), so nothing is written to stdout here.
func runEdit(root, slug string, mentions map[string]string) (string, error) {
	posts, err := scanPosts(root)
	if err != nil {
		return "", err
	}
	var target *post
	for i := range posts {
		if posts[i].Slug == slug {
			target = &posts[i]
			break
		}
	}
	if target == nil {
		return "", fmt.Errorf("no post named %q under %s/", slug, contentPostsDir)
	}
	if !target.HasCompanion {
		return "", fmt.Errorf("%q has no %s", slug, companionFile)
	}

	st, err := loadState(root)
	if err != nil {
		return "", err
	}
	entry, ok := st.Posts[slug]
	if !ok || entry.Note == "" {
		return "", fmt.Errorf("%q has no recorded LinkedIn URN in %s — publish it first", slug, stateFileName)
	}

	body, err := os.ReadFile(target.CompanionPath)
	if err != nil {
		return "", fmt.Errorf("read companion: %w", err)
	}
	commentary := strings.TrimSpace(string(body))
	if commentary == "" {
		return "", fmt.Errorf("%s is empty", target.CompanionPath)
	}
	commentary = applyMentions(commentary, mentions)
	if n := commentaryUTF16Len(commentary); n > linkedinCommentaryMaxUTF16 {
		return "", fmt.Errorf("companion %s is %d UTF-16 units, over LinkedIn's %d limit — trim it before editing", target.CompanionPath, n, linkedinCommentaryMaxUTF16)
	}

	toks, err := loadTokens()
	if err != nil {
		return "", err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return "", err
	}

	if err := editLinkedInPostCommentary(toks.AccessToken, entry.Note, commentary); err != nil {
		return "", err
	}
	return entry.Note, nil
}

// runRepublish deletes the existing LinkedIn post and creates a fresh one. This
// is the only way to change a published post's article card (e.g. after fixing
// the page's Open Graph image): editing commentary in place cannot. The new
// post runs the full preflight and gets a new URN recorded in the state file.
func runRepublish(root, slug, at string, noVerify bool, baseURL string, mentions map[string]string, rep Reporter) (PublishResult, error) {
	st, err := loadState(root)
	if err != nil {
		return PublishResult{}, err
	}
	entry, ok := st.Posts[slug]
	if !ok || entry.Note == "" {
		return PublishResult{}, fmt.Errorf("%q has no recorded LinkedIn URN in %s — use `publish` instead", slug, stateFileName)
	}

	toks, err := loadTokens()
	if err != nil {
		return PublishResult{}, err
	}
	toks, err = ensureFreshTokens(toks)
	if err != nil {
		return PublishResult{}, err
	}

	if err := deleteLinkedInPost(toks.AccessToken, entry.Note); err != nil {
		return PublishResult{}, fmt.Errorf("delete existing post %s: %w", entry.Note, err)
	}
	rep.Stepf("deleted old post %s — creating a fresh one...", entry.Note)

	// force=true overwrites the stale state entry with the new URN.
	return runPublish(root, slug, at, true, false, noVerify, baseURL, mentions, rep)
}

func buildPostPayload(commentary, articleURL, title, description, thumbnailURN string, scheduled bool, publishAt time.Time) map[string]any {
	article := map[string]any{
		"source": articleURL,
		"title":  title,
	}
	if description != "" {
		article["description"] = description
	}
	// The Posts API never scrapes og:image — without an uploaded thumbnail the
	// article card has no picture. See uploadImage.
	if thumbnailURN != "" {
		article["thumbnail"] = thumbnailURN
	}
	payload := map[string]any{
		"commentary": commentary,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []string{},
			"thirdPartyDistributionChannels": []string{},
		},
		"content":                   map[string]any{"article": article},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	if scheduled {
		payload["publishedAt"] = publishAt.UnixMilli()
	}
	return payload
}
