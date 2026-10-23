package source

import (
	"context"
	"encoding/xml"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Reddit reads Reddit's public Atom feeds: posts linking each domain, a search
// for each keyword, and optionally the private inbox feeds of the user's
// accounts (replies to their own posts and comments) from reddit.com/prefs/feeds. No API app is
// needed. Reddit rate-limits feeds, so requests are spaced out, and a refused
// request is reported and retried on the next check.
type Reddit struct {
	BaseURL    string   // defaults to https://www.reddit.com
	InboxFeeds []string // optional private inbox feed URLs (each works like a password)
	Pause      time.Duration
}

func (Reddit) Name() string { return "reddit" }

// redditBoilerplate is the footer Reddit adds to every feed entry.
var redditBoilerplate = regexp.MustCompile(`(?s)submitted by\s+/u/\S+(\s+to\s+\S+)?\s*\[link\]\s*\[comments\]`)

type atomFeed struct {
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
		Author    struct {
			Name string `xml:"name"`
		} `xml:"author"`
		Category struct {
			Term string `xml:"term,attr"`
		} `xml:"category"`
		Link struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Content string `xml:"content"`
	} `xml:"entry"`
}

func (r Reddit) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	base := r.BaseURL
	if base == "" {
		base = "https://www.reddit.com"
	}
	pause := r.Pause
	if pause == 0 {
		pause = 3 * time.Second
	}
	var feeds []string
	preset := map[string][]string{} // feed URL -> terms its items match by definition
	for _, d := range w.Domains {
		u := base + "/domain/" + url.PathEscape(d) + "/new/.rss?limit=50"
		feeds = append(feeds, u)
		preset[u] = []string{d}
	}
	for _, k := range append(append([]string{}, w.Keywords...), w.Competitors...) {
		feeds = append(feeds, base+"/search.rss?"+url.Values{"q": {`"` + k + `"`}, "sort": {"new"}, "t": {"week"}, "limit": {"50"}}.Encode())
	}
	inboxOf := map[string]string{} // feed URL -> the account it belongs to
	for _, f := range r.InboxFeeds {
		feeds = append(feeds, f)
		inboxOf[f] = InboxUser(f)
	}
	seen := map[string]bool{}
	var items []Item
	var firstErr error
	for i, feedURL := range feeds {
		if i > 0 {
			select {
			case <-ctx.Done():
				return items, ctx.Err()
			case <-time.After(pause):
			}
		}
		body, err := get(ctx, feedURL, nil)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		var feed atomFeed
		if err := xml.Unmarshal(body, &feed); err != nil {
			continue
		}
		inboxUser, inbox := inboxOf[feedURL]
		for _, e := range feed.Entries {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			created, _ := time.Parse(time.RFC3339, firstNonEmpty(e.Published, e.Updated))
			where := "r/" + e.Category.Term
			if term := e.Category.Term; strings.HasPrefix(term, "u_") || strings.HasPrefix(term, "u/") {
				where = "u/" + term[2:] + " (profile)"
			}
			if inbox {
				where = "your inbox"
				if inboxUser != "" {
					where += " (u/" + inboxUser + ")"
				}
			}
			items = append(items, Item{
				Source:     "reddit",
				ExternalID: e.ID,
				URL:        e.Link.Href,
				Author:     strings.TrimPrefix(e.Author.Name, "/u/"),
				Title:      e.Title,
				Text:       strings.TrimSpace(redditBoilerplate.ReplaceAllString(plain(e.Content), "")),
				Where:      where,
				Created:    created.UTC(),
				Terms:      preset[feedURL],
				Account:    accountFor(inbox, inboxUser),
			})
		}
	}
	return items, firstErr
}

// Lobsters reads the public feed of stories linking each domain.
type Lobsters struct {
	BaseURL string // defaults to https://lobste.rs
}

func (Lobsters) Name() string { return "lobsters" }

type rssFeed struct {
	Items []struct {
		GUID     string `xml:"guid"`
		Title    string `xml:"title"`
		Link     string `xml:"link"`
		Comments string `xml:"comments"`
		Author   string `xml:"author"`
		PubDate  string `xml:"pubDate"`
	} `xml:"channel>item"`
}

func (l Lobsters) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	base := l.BaseURL
	if base == "" {
		base = "https://lobste.rs"
	}
	var items []Item
	for _, d := range w.Domains {
		body, err := get(ctx, base+"/domains/"+url.PathEscape(d)+".rss", nil)
		if err != nil {
			return items, err
		}
		var feed rssFeed
		if err := xml.Unmarshal(body, &feed); err != nil {
			return items, err
		}
		for _, it := range feed.Items {
			created, _ := time.Parse(time.RFC1123Z, it.PubDate)
			author := it.Author
			if i := strings.Index(author, " via "); i >= 0 {
				author = author[i+5:]
			}
			if i := strings.Index(author, "@"); i >= 0 {
				author = author[:i]
			}
			items = append(items, Item{
				Source:     "lobsters",
				ExternalID: it.GUID,
				URL:        firstNonEmpty(it.Comments, it.Link),
				Author:     strings.TrimSpace(author),
				Title:      it.Title,
				Text:       it.Link,
				Where:      "story",
				Created:    created.UTC(),
				Terms:      []string{d},
			})
		}
	}
	return items, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// InboxUser reads the account name from a private feed URL (its user= parameter).
func InboxUser(feedURL string) string {
	u, err := url.Parse(feedURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("user")
}

func accountFor(inbox bool, user string) string {
	if !inbox {
		return ""
	}
	return "reddit:" + user
}
