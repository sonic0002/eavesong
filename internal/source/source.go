// Package source holds the connectors that find mentions of a product on
// public sites. Every connector only reads; nothing here posts or votes.
package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Watch is what to look for: the product's domains (links to the site), its
// keywords (the product name and close variants), and competitor names.
type Watch struct {
	Domains     []string
	Keywords    []string
	Competitors []string
}

// Item is one post or comment found on a source.
type Item struct {
	Source     string // "hackernews", "reddit", ...
	ExternalID string // unique within the source
	URL        string // where to read and reply
	Author     string
	Title      string    // story or thread title, when there is one
	Text       string    // the post or comment text, plain
	Where      string    // "r/golang", "story", "comment", ...
	Created    time.Time // when it was posted
	// Terms the source already knows the item matches, for example the domain
	// of a "posts linking this domain" feed whose text may not contain the link.
	Terms []string
	// What the item replies to, quotes or comments on, when the source knows:
	// that post's text and a link to it.
	ContextText string
	ContextURL  string
	ContextOwn  bool // the replied-to post or article is the user's own
	// Account is set on replies and mentions that arrived through one of the
	// user's own accounts ("bluesky:acme.dev"), so they only reach the
	// projects that account is used for. Empty for search results.
	Account string
}

// Source is a connector. Fetch returns recent items that may match the watch;
// the caller decides which terms each item really mentions.
type Source interface {
	Name() string
	Fetch(ctx context.Context, w Watch) ([]Item, error)
}

// UserAgent identifies the service to the sites it reads.
const UserAgent = "eavesong/0.1 (self-hosted product monitor; read-only; +https://github.com/sonic0002/eavesong)"

var client = &http.Client{Timeout: 30 * time.Second}

// get fetches a URL and returns the body, or an error that includes the status.
func get(ctx context.Context, url string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		if strings.HasPrefix(k, "X-Eavesong-") {
			continue // internal values passed alongside headers, never sent
		}
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

// terms is every term in a watch, for sources that search by text.
func (w Watch) terms() []string {
	var out []string
	out = append(out, w.Domains...)
	out = append(out, w.Keywords...)
	out = append(out, w.Competitors...)
	return out
}

// Contains reports whether text mentions term, ignoring case. Terms that look
// like words must match at word boundaries, so "go" does not match "google".
func Contains(text, term string) bool {
	text, term = strings.ToLower(text), strings.ToLower(strings.TrimSpace(term))
	if term == "" {
		return false
	}
	for start := 0; ; {
		i := strings.Index(text[start:], term)
		if i < 0 {
			return false
		}
		i += start
		before := i == 0 || !isWordByte(text[i-1])
		end := i + len(term)
		after := end == len(text) || !isWordByte(text[end])
		if before && after {
			return true
		}
		start = i + 1
	}
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
