package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// HackerNews searches stories and comments through the public Algolia API.
// No account or key is needed.
type HackerNews struct {
	BaseURL string // defaults to https://hn.algolia.com
}

func (HackerNews) Name() string { return "hackernews" }

type hnHit struct {
	ObjectID    string   `json:"objectID"`
	Author      string   `json:"author"`
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	StoryText   string   `json:"story_text"`
	CommentText string   `json:"comment_text"`
	StoryTitle  string   `json:"story_title"`
	StoryID     int      `json:"story_id"`
	CreatedAtI  int64    `json:"created_at_i"`
	Tags        []string `json:"_tags"`
}

var tagPattern = regexp.MustCompile(`<[^>]+>`)

// plain turns the small HTML subset HN uses into text.
func plain(s string) string {
	s = strings.ReplaceAll(s, "<p>", "\n\n")
	return strings.TrimSpace(html.UnescapeString(tagPattern.ReplaceAllString(s, "")))
}

func (h HackerNews) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	base := h.BaseURL
	if base == "" {
		base = "https://hn.algolia.com"
	}
	since := time.Now().Add(-7 * 24 * time.Hour).Unix()
	seen := map[string]bool{}
	var items []Item
	for _, term := range w.terms() {
		q := url.Values{
			"query":          {term},
			"tags":           {"(story,comment)"},
			"numericFilters": {fmt.Sprintf("created_at_i>%d", since)},
			"hitsPerPage":    {"50"},
		}
		body, err := get(ctx, base+"/api/v1/search_by_date?"+q.Encode(), nil)
		if err != nil {
			return items, err
		}
		var result struct {
			Hits []hnHit `json:"hits"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return items, fmt.Errorf("hackernews: %w", err)
		}
		for _, hit := range result.Hits {
			if seen[hit.ObjectID] {
				continue
			}
			seen[hit.ObjectID] = true
			item := Item{
				Source:     "hackernews",
				ExternalID: hit.ObjectID,
				URL:        "https://news.ycombinator.com/item?id=" + hit.ObjectID,
				Author:     hit.Author,
				Created:    time.Unix(hit.CreatedAtI, 0).UTC(),
			}
			if hit.CommentText != "" {
				item.Where = "comment"
				item.Title = hit.StoryTitle
				item.Text = plain(hit.CommentText)
				if hit.StoryID != 0 {
					item.ContextURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%d", hit.StoryID)
				}
			} else {
				item.Where = "story"
				item.Title = hit.Title
				item.Text = strings.TrimSpace(hit.URL + "\n" + plain(hit.StoryText))
			}
			items = append(items, item)
		}
	}
	return items, nil
}
