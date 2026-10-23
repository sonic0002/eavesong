package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BlueskyAccount is one signed-in Bluesky account: a handle and an app password
// (Settings > App passwords), never the account's main password.
type BlueskyAccount struct {
	Handle      string
	AppPassword string
	Key         string // "bluesky:<handle>", copied to the items it brings in
}

// Bluesky searches posts and reads each account's notifications. Bluesky's
// search needs a signed-in account, so search uses the first account that signs
// in; every account's replies, mentions and quotes are read as well. It only reads.
type Bluesky struct {
	Service  string // defaults to https://bsky.social
	Accounts []BlueskyAccount
}

func (Bluesky) Name() string { return "bluesky" }

func (b Bluesky) service() string {
	if b.Service == "" {
		return "https://bsky.social"
	}
	return b.Service
}

func (b Bluesky) signIn(ctx context.Context, acct BlueskyAccount) (map[string]string, error) {
	payload, _ := json.Marshal(map[string]string{"identifier": acct.Handle, "password": acct.AppPassword})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.service()+"/xrpc/com.atproto.server.createSession", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var session struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil || resp.StatusCode != http.StatusOK || session.AccessJwt == "" {
		return nil, fmt.Errorf("bluesky: @%s could not sign in (HTTP %d); check the app password", acct.Handle, resp.StatusCode)
	}
	return map[string]string{"Authorization": "Bearer " + session.AccessJwt, didHeader: session.DID}, nil
}

type bskyPost struct {
	URI    string `json:"uri"`
	Author struct {
		Handle string `json:"handle"`
		DID    string `json:"did"`
	} `json:"author"`
	Record struct {
		Text      string `json:"text"`
		CreatedAt string `json:"createdAt"`
	} `json:"record"`
}

func (p bskyPost) webURL() string {
	return "https://bsky.app/profile/" + p.Author.Handle + "/post/" + p.URI[strings.LastIndex(p.URI, "/")+1:]
}

// posts looks up posts by URI (25 per request) and returns them by URI.
// Lookup failures only mean the cards go without context.
func (b Bluesky) posts(ctx context.Context, auth map[string]string, byNote map[string]string) map[string]bskyPost {
	var uris []string
	wanted := map[string]bool{}
	for _, u := range byNote {
		if !wanted[u] {
			wanted[u] = true
			uris = append(uris, u)
		}
	}
	out := map[string]bskyPost{}
	for start := 0; start < len(uris); start += 25 {
		q := url.Values{}
		for _, u := range uris[start:min(start+25, len(uris))] {
			q.Add("uris", u)
		}
		body, err := get(ctx, b.service()+"/xrpc/app.bsky.feed.getPosts?"+q.Encode(), auth)
		if err != nil {
			return out
		}
		var result struct {
			Posts []bskyPost `json:"posts"`
		}
		if json.Unmarshal(body, &result) == nil {
			for _, p := range result.Posts {
				out[p.URI] = p
			}
		}
	}
	return out
}

func (p bskyPost) item(where string, terms []string) Item {
	created, _ := time.Parse(time.RFC3339, p.Record.CreatedAt)
	rkey := p.URI[strings.LastIndex(p.URI, "/")+1:]
	return Item{
		Source: "bluesky", ExternalID: p.URI, URL: "https://bsky.app/profile/" + p.Author.Handle + "/post/" + rkey,
		Author: p.Author.Handle, Text: p.Record.Text, Where: where, Created: created.UTC(), Terms: terms,
	}
}

func (b Bluesky) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(b.Accounts) == 0 {
		return nil, errors.New("bluesky: add an account in Settings")
	}
	seen := map[string]bool{}
	var items []Item
	var problems []error
	var searchAuth map[string]string
	for _, acct := range b.Accounts {
		auth, err := b.signIn(ctx, acct)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if searchAuth == nil {
			searchAuth = auth
		}
		// Replies, mentions and quotes of this account are about you by definition.
		body, err := get(ctx, b.service()+"/xrpc/app.bsky.notification.listNotifications?limit=50", auth)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		var notes struct {
			Notifications []struct {
				URI    string `json:"uri"`
				Reason string `json:"reason"`
				Author struct {
					Handle string `json:"handle"`
				} `json:"author"`
				Record struct {
					Text      string `json:"text"`
					CreatedAt string `json:"createdAt"`
					Reply     struct {
						Parent struct {
							URI string `json:"uri"`
						} `json:"parent"`
					} `json:"reply"`
					Embed struct {
						Record struct {
							URI    string `json:"uri"`
							Record struct {
								URI string `json:"uri"`
							} `json:"record"` // quote with media
						} `json:"record"`
					} `json:"embed"`
				} `json:"record"`
			} `json:"notifications"`
		}
		if err := json.Unmarshal(body, &notes); err != nil {
			problems = append(problems, err)
			continue
		}
		parents := map[string]string{} // notification URI -> the post it replies to or quotes
		for _, n := range notes.Notifications {
			if (n.Reason != "reply" && n.Reason != "mention" && n.Reason != "quote") || seen[n.URI] {
				continue
			}
			seen[n.URI] = true
			p := bskyPost{URI: n.URI}
			p.Author.Handle, p.Record.Text, p.Record.CreatedAt = n.Author.Handle, n.Record.Text, n.Record.CreatedAt
			it := p.item("your notifications (@"+acct.Handle+")", nil)
			it.Account = acct.Key
			items = append(items, it)
			if parent := firstNonEmpty(n.Record.Reply.Parent.URI, n.Record.Embed.Record.URI, n.Record.Embed.Record.Record.URI); strings.HasPrefix(parent, "at://") {
				parents[n.URI] = parent
			}
		}
		// Fetch the replied-to posts in batches, so each card can show them.
		replied := b.posts(ctx, auth, parents)
		for i := range items {
			if c, ok := replied[parents[items[i].ExternalID]]; ok {
				items[i].ContextText, items[i].ContextURL = c.Record.Text, c.webURL()
				// Their post answers this account's own post when the parent URI is in its repo.
				items[i].ContextOwn = ownURI(parents[items[i].ExternalID], auth) || strings.EqualFold(c.Author.Handle, acct.Handle)
			}
		}
	}
	if searchAuth == nil {
		return items, errors.Join(problems...)
	}
	for _, term := range w.terms() {
		q := url.Values{"q": {term}, "sort": {"latest"}, "limit": {"50"}}
		var preset []string
		for _, d := range w.Domains {
			if d == term {
				q.Set("domain", d) // posts linking the domain, not just naming it
				preset = []string{d}
			}
		}
		body, err := get(ctx, b.service()+"/xrpc/app.bsky.feed.searchPosts?"+q.Encode(), searchAuth)
		if err != nil {
			problems = append(problems, err)
			break
		}
		var result struct {
			Posts []bskyPost `json:"posts"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			problems = append(problems, fmt.Errorf("bluesky: %w", err))
			break
		}
		for _, p := range result.Posts {
			if !seen[p.URI] {
				seen[p.URI] = true
				items = append(items, p.item("post", preset))
			}
		}
	}
	return items, errors.Join(problems...)
}

// GitHub searches issues, pull requests and discussions that mention a term.
// It works without a token (10 searches a minute); with tokens it uses the
// first and moves to the next when one is rate-limited or rejected.
type GitHub struct {
	BaseURL string   // defaults to https://api.github.com
	Tokens  []string // optional, read-only is enough
}

func (GitHub) Name() string { return "github" }

func (g GitHub) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	tokens := append([]string{}, g.Tokens...)
	headers := func() map[string]string {
		h := map[string]string{"Accept": "application/vnd.github+json"}
		if len(tokens) > 0 {
			h["Authorization"] = "Bearer " + tokens[0]
		}
		return h
	}
	limited := func(err error) bool {
		msg := err.Error()
		return strings.Contains(msg, "HTTP 401") || strings.Contains(msg, "HTTP 403") || strings.Contains(msg, "HTTP 429")
	}
	since := time.Now().Add(-7 * 24 * time.Hour).Format("2006-01-02")
	var items []Item
	for _, term := range w.terms() {
		q := url.Values{"q": {fmt.Sprintf(`"%s" created:>=%s`, term, since)}, "sort": {"created"}, "per_page": {"30"}}
		body, err := get(ctx, base+"/search/issues?"+q.Encode(), headers())
		for err != nil && len(tokens) > 1 && limited(err) {
			tokens = tokens[1:] // this token is limited or invalid; try the next
			body, err = get(ctx, base+"/search/issues?"+q.Encode(), headers())
		}
		if err != nil {
			return items, err
		}
		var result struct {
			Items []struct {
				ID        int64  `json:"id"`
				HTMLURL   string `json:"html_url"`
				Title     string `json:"title"`
				Body      string `json:"body"`
				CreatedAt string `json:"created_at"`
				User      struct {
					Login string `json:"login"`
				} `json:"user"`
				PullRequest *struct{} `json:"pull_request"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return items, fmt.Errorf("github: %w", err)
		}
		for _, it := range result.Items {
			created, _ := time.Parse(time.RFC3339, it.CreatedAt)
			where := "issue"
			if it.PullRequest != nil {
				where = "pull request"
			}
			if parts := strings.Split(strings.TrimPrefix(it.HTMLURL, "https://github.com/"), "/"); len(parts) >= 2 {
				where += " in " + parts[0] + "/" + parts[1]
			}
			items = append(items, Item{
				Source: "github", ExternalID: fmt.Sprint(it.ID), URL: it.HTMLURL, Author: it.User.Login,
				Title: it.Title, Text: it.Body, Where: where, Created: created.UTC(),
			})
		}
	}
	return items, nil
}

// VerifyBluesky signs in once and returns the account's handle as Bluesky
// knows it, so a DID, an email or a short name is saved as the real handle.
func VerifyBluesky(ctx context.Context, service, identifier, appPassword string) (string, error) {
	b := Bluesky{Service: service}
	payload, _ := json.Marshal(map[string]string{"identifier": identifier, "password": appPassword})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.service()+"/xrpc/com.atproto.server.createSession", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Bluesky: %w", err)
	}
	defer resp.Body.Close()
	var session struct {
		Handle string `json:"handle"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return "", errors.New("Bluesky did not accept that handle and app password")
	case resp.StatusCode != http.StatusOK || session.Handle == "":
		return "", fmt.Errorf("Bluesky sign-in failed (HTTP %d)", resp.StatusCode)
	}
	return session.Handle, nil
}

// VerifyGitHub checks a token and returns the login it belongs to.
func VerifyGitHub(ctx context.Context, baseURL, token string) (string, error) {
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	body, err := get(ctx, baseURL+"/user", map[string]string{"Accept": "application/vnd.github+json", "Authorization": "Bearer " + token})
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			return "", errors.New("GitHub did not accept that token")
		}
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	json.Unmarshal(body, &user)
	return user.Login, nil
}

// didHeader carries the signed-in account's DID alongside its token; get
// never sends headers with this prefix.
const didHeader = "X-Eavesong-Did"

// ownURI reports whether an at:// URI is in the signed-in account's repository.
func ownURI(uri string, auth map[string]string) bool {
	did := auth[didHeader]
	return did != "" && strings.HasPrefix(uri, "at://"+did+"/")
}
