package source

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Paced is implemented by sources that must not run on every check, because
// their API has a small free quota or charges per request.
type Paced interface {
	MinInterval() time.Duration
}

// Budget limits how many paid reads a source may make per day.
type Budget interface {
	Left(ctx context.Context) int
	Spend(ctx context.Context, n int)
}

// ---------------------------------------------------------------- Stack Overflow

// StackOverflow searches questions on Stack Overflow through the free Stack
// Exchange API. A key (stackapps.com) raises the daily quota from 300 to 10,000.
type StackOverflow struct {
	BaseURL string // defaults to https://api.stackexchange.com/2.3
	Key     string
}

func (StackOverflow) Name() string { return "stackoverflow" }

func (s StackOverflow) MinInterval() time.Duration {
	if s.Key == "" {
		return 2 * time.Hour // keeps a keyless install inside 300 requests a day
	}
	return 0
}

func (s StackOverflow) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	base := s.BaseURL
	if base == "" {
		base = "https://api.stackexchange.com/2.3"
	}
	since := time.Now().Add(-30 * 24 * time.Hour).Unix() // Stack Overflow is slower than social sites
	seen := map[int64]bool{}
	var items []Item
	for _, term := range w.terms() {
		q := url.Values{"order": {"desc"}, "sort": {"creation"}, "q": {term}, "site": {"stackoverflow"},
			"fromdate": {fmt.Sprint(since)}, "pagesize": {"50"}, "filter": {"withbody"}}
		if s.Key != "" {
			q.Set("key", s.Key)
		}
		body, err := get(ctx, base+"/search/advanced?"+q.Encode(), nil)
		if err != nil {
			return items, fmt.Errorf("stackoverflow: %w", err)
		}
		var result struct {
			Items []struct {
				QuestionID   int64  `json:"question_id"`
				Title        string `json:"title"`
				Body         string `json:"body"`
				Link         string `json:"link"`
				CreationDate int64  `json:"creation_date"`
				IsAnswered   bool   `json:"is_answered"`
				Owner        struct {
					DisplayName string `json:"display_name"`
				} `json:"owner"`
			} `json:"items"`
			ErrorMessage string `json:"error_message"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return items, fmt.Errorf("stackoverflow: %w", err)
		}
		for _, q := range result.Items {
			if seen[q.QuestionID] {
				continue
			}
			seen[q.QuestionID] = true
			where := "question"
			if !q.IsAnswered {
				where = "question, not answered yet"
			}
			items = append(items, Item{
				Source: "stackoverflow", ExternalID: fmt.Sprint(q.QuestionID), URL: q.Link, Author: plain(q.Owner.DisplayName),
				Title: plain(q.Title), Text: plain(q.Body), Where: where, Created: time.Unix(q.CreationDate, 0).UTC(),
			})
		}
	}
	return items, nil
}

// VerifyStackExchange checks a Stack Exchange key with one cheap request.
func VerifyStackExchange(ctx context.Context, baseURL, key string) error {
	if baseURL == "" {
		baseURL = "https://api.stackexchange.com/2.3"
	}
	_, err := get(ctx, baseURL+"/info?site=stackoverflow&key="+url.QueryEscape(key), nil)
	if err != nil && strings.Contains(err.Error(), "HTTP 400") {
		return errors.New("Stack Exchange did not accept that key")
	}
	return err
}

// ---------------------------------------------------------------- Google Alerts

// GoogleAlerts reads Google Alerts that the user set to deliver as an RSS feed
// (google.com/alerts, "Deliver to: RSS feed"). They catch blogs, news and
// forums that no other source covers.
type GoogleAlerts struct {
	Feeds []string
}

func (GoogleAlerts) Name() string { return "googlealerts" }

type alertFeed struct {
	Title   string `xml:"title"`
	Entries []struct {
		ID        string `xml:"id"`
		Title     string `xml:"title"`
		Published string `xml:"published"`
		Link      struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Content string `xml:"content"`
	} `xml:"entry"`
}

// alertTarget turns Google's redirect link into the page it points to.
func alertTarget(link string) string {
	u, err := url.Parse(link)
	if err == nil && strings.HasSuffix(u.Host, "google.com") && u.Query().Get("url") != "" {
		return u.Query().Get("url")
	}
	return link
}

func readAlertFeed(ctx context.Context, feedURL string) (alertFeed, error) {
	var feed alertFeed
	body, err := get(ctx, feedURL, nil)
	if err != nil {
		return feed, err
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return feed, errors.New("that link is not a Google Alerts feed")
	}
	return feed, nil
}

func (g GoogleAlerts) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	var items []Item
	var problems []error
	for _, f := range g.Feeds {
		feed, err := readAlertFeed(ctx, f)
		if err != nil {
			problems = append(problems, fmt.Errorf("googlealerts: %w", err))
			continue
		}
		alert := strings.TrimPrefix(plain(feed.Title), "Google Alert - ")
		for _, e := range feed.Entries {
			created, _ := time.Parse(time.RFC3339, e.Published)
			target := alertTarget(e.Link.Href)
			site := target
			if u, err := url.Parse(target); err == nil {
				site = strings.TrimPrefix(u.Host, "www.")
			}
			items = append(items, Item{
				Source: "googlealerts", ExternalID: e.ID, URL: target, Author: site,
				Title: plain(e.Title), Text: plain(e.Content), Where: "Google Alert for " + alert, Created: created.UTC(),
				Terms: []string{alert},
			})
		}
	}
	return items, errors.Join(problems...)
}

// VerifyGoogleAlert reads a feed once and returns what the alert is for.
func VerifyGoogleAlert(ctx context.Context, feedURL string) (string, error) {
	u, err := url.Parse(feedURL)
	if err != nil || !strings.HasSuffix(u.Host, "google.com") || !strings.HasPrefix(u.Path, "/alerts/feeds/") {
		return "", errors.New("paste the RSS link of an alert from google.com/alerts (set \"Deliver to\" to RSS feed)")
	}
	feed, err := readAlertFeed(ctx, feedURL)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(plain(feed.Title), "Google Alert - "), nil
}

// ---------------------------------------------------------------- Mastodon

// MastodonAccount is an account on a Mastodon server with an access token
// that has the "read" scope (Preferences > Development > New application).
type MastodonAccount struct {
	Server string // e.g. https://mastodon.social
	Acct   string // username@server
	Token  string
}

func (a MastodonAccount) key() string { return "mastodon:" + a.Acct }

// Mastodon searches public posts and reads each account's mentions. Most
// servers only let signed-in accounts search, so it needs an account.
type Mastodon struct {
	Accounts []MastodonAccount
}

func (Mastodon) Name() string { return "mastodon" }

type mastoStatus struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	URI       string `json:"uri"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
	Account   struct {
		Acct string `json:"acct"`
	} `json:"account"`
}

func (s mastoStatus) item(where string) Item {
	created, _ := time.Parse(time.RFC3339, s.CreatedAt)
	return Item{Source: "mastodon", ExternalID: s.URI, URL: firstNonEmpty(s.URL, s.URI), Author: s.Account.Acct,
		Text: plain(s.Content), Where: where, Created: created.UTC()}
}

func (m Mastodon) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(m.Accounts) == 0 {
		return nil, errors.New("mastodon: add an account in Settings")
	}
	seen := map[string]bool{}
	var items []Item
	var problems []error
	add := func(s mastoStatus, where, account string) {
		if !seen[s.URI] {
			seen[s.URI] = true
			it := s.item(where)
			it.Account = account
			items = append(items, it)
		}
	}
	for i, a := range m.Accounts {
		auth := map[string]string{"Authorization": "Bearer " + a.Token}
		body, err := get(ctx, a.Server+"/api/v1/notifications?types[]=mention&limit=40", auth)
		if err != nil {
			problems = append(problems, fmt.Errorf("mastodon: %s: %w", a.Acct, err))
			continue
		}
		var notes []struct {
			Status *mastoStatus `json:"status"`
		}
		if json.Unmarshal(body, &notes) == nil {
			for _, n := range notes {
				if n.Status != nil {
					add(*n.Status, "your mentions (@"+a.Acct+")", a.key())
				}
			}
		}
		if i > 0 {
			continue // one account's search covers the whole network
		}
		for _, term := range w.terms() {
			body, err := get(ctx, a.Server+"/api/v2/search?"+url.Values{"q": {term}, "type": {"statuses"}, "limit": {"40"}}.Encode(), auth)
			if err != nil {
				problems = append(problems, fmt.Errorf("mastodon search: %w", err))
				break
			}
			var result struct {
				Statuses []mastoStatus `json:"statuses"`
			}
			if json.Unmarshal(body, &result) == nil {
				for _, s := range result.Statuses {
					add(s, "post", "")
				}
			}
		}
	}
	return items, errors.Join(problems...)
}

// VerifyMastodon checks a token on a server and returns the account as user@server.
func VerifyMastodon(ctx context.Context, server, token string) (string, error) {
	body, err := get(ctx, server+"/api/v1/accounts/verify_credentials", map[string]string{"Authorization": "Bearer " + token})
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "HTTP 403") {
			return "", errors.New("that Mastodon server did not accept the access token")
		}
		return "", fmt.Errorf("could not reach that Mastodon server: %w", err)
	}
	var acct struct {
		Username string `json:"username"`
	}
	json.Unmarshal(body, &acct)
	u, _ := url.Parse(server)
	return acct.Username + "@" + u.Host, nil
}

// ---------------------------------------------------------------- X

// XAccount is an X API bearer token from the user's own developer account,
// and optionally the username whose mentions to read.
type XAccount struct {
	Username string
	Bearer   string
}

// X searches with the shared bearer tokens and reads the mentions of the
// user's own accounts (Usernames).
func (a XAccount) key() string { return "xaccount:" + a.Username }

// X searches recent posts and reads mentions through the official API with
// the user's own token. X charges per post read, so reads are capped by a
// daily budget and the source runs at most every two hours.
type X struct {
	BaseURL   string   // defaults to https://api.x.com/2
	Tokens    []string // bearer tokens from the user's X developer account
	Usernames []string // the user's own X accounts, whose mentions are read
	Budget    Budget
}

func (X) Name() string               { return "x" }
func (X) MinInterval() time.Duration { return 2 * time.Hour }

type xResponse struct {
	Data []struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		AuthorID  string `json:"author_id"`
		CreatedAt string `json:"created_at"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	} `json:"includes"`
}

func (r xResponse) items(where string, terms []string) []Item {
	names := map[string]string{}
	for _, u := range r.Includes.Users {
		names[u.ID] = u.Username
	}
	var out []Item
	for _, t := range r.Data {
		created, _ := time.Parse(time.RFC3339, t.CreatedAt)
		author := names[t.AuthorID]
		out = append(out, Item{Source: "x", ExternalID: t.ID, URL: "https://x.com/" + firstNonEmpty(author, "i") + "/status/" + t.ID,
			Author: author, Text: t.Text, Where: where, Created: created.UTC(), Terms: terms})
	}
	return out
}

func (x X) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(x.Tokens) == 0 {
		return nil, errors.New("x: add an API token in Settings")
	}
	base := x.BaseURL
	if base == "" {
		base = "https://api.x.com/2"
	}
	fields := url.Values{"tweet.fields": {"created_at,author_id"}, "expansions": {"author_id"}, "user.fields": {"username"}}
	read := func(path string, q url.Values) (xResponse, error) {
		var r xResponse
		left := 100
		if x.Budget != nil {
			left = x.Budget.Left(ctx)
		}
		if left < 10 {
			return r, errors.New("x: today's read budget is used up; it resets at midnight UTC")
		}
		for k, v := range fields {
			q[k] = v
		}
		q.Set("max_results", fmt.Sprint(min(left, 100)))
		body, err := get(ctx, base+path+"?"+q.Encode(), map[string]string{"Authorization": "Bearer " + x.Tokens[0]})
		if err != nil {
			return r, fmt.Errorf("x: %w", err)
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return r, err
		}
		if x.Budget != nil {
			x.Budget.Spend(ctx, len(r.Data)+len(r.Includes.Users))
		}
		return r, nil
	}
	var items []Item
	var problems []error
	// One search for every term, to spend as few reads as possible.
	var parts []string
	for _, t := range w.terms() {
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, ``)+`"`)
	}
	if len(parts) > 0 {
		q := url.Values{"query": {"(" + strings.Join(parts, " OR ") + ") -is:retweet"}}
		if r, err := read("/tweets/search/recent", q); err != nil {
			problems = append(problems, err)
		} else {
			items = append(items, r.items("post", nil)...)
		}
	}
	for _, user := range x.Usernames {
		q := url.Values{"query": {"@" + user + " -from:" + user + " -is:retweet"}}
		if r, err := read("/tweets/search/recent", q); err != nil {
			problems = append(problems, err)
		} else {
			for _, it := range r.items("your mentions (@"+user+")", nil) {
				it.Account = XAccount{Username: user}.key()
				items = append(items, it)
			}
		}
	}
	return items, errors.Join(problems...)
}

// VerifyX checks a bearer token by looking up one username (one paid read).
func VerifyX(ctx context.Context, baseURL, bearer, username string) error {
	if baseURL == "" {
		baseURL = "https://api.x.com/2"
	}
	lookup := firstNonEmpty(username, "X")
	_, err := get(ctx, baseURL+"/users/by/username/"+url.PathEscape(lookup), map[string]string{"Authorization": "Bearer " + bearer})
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "HTTP 401"):
		return errors.New("X did not accept that bearer token")
	case strings.Contains(err.Error(), "HTTP 402"), strings.Contains(err.Error(), "HTTP 403"):
		return errors.New("X accepted the token but the developer account has no credit or access to this endpoint")
	}
	return err
}

// ---------------------------------------------------------------- YouTube

// YouTube searches recent videos with the user's own Data API key. A search
// costs 100 of the free 10,000 daily quota units, so it runs every six hours.
type YouTube struct {
	BaseURL string // defaults to https://www.googleapis.com/youtube/v3
	Keys    []string
}

func (YouTube) Name() string               { return "youtube" }
func (YouTube) MinInterval() time.Duration { return 6 * time.Hour }

func (y YouTube) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(y.Keys) == 0 {
		return nil, errors.New("youtube: add an API key in Settings")
	}
	base := y.BaseURL
	if base == "" {
		base = "https://www.googleapis.com/youtube/v3"
	}
	since := time.Now().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	seen := map[string]bool{}
	var items []Item
	for _, term := range w.terms() {
		q := url.Values{"part": {"snippet"}, "type": {"video"}, "order": {"date"}, "q": {term}, "publishedAfter": {since},
			"maxResults": {"25"}, "key": {y.Keys[0]}}
		body, err := get(ctx, base+"/search?"+q.Encode(), nil)
		if err != nil {
			return items, fmt.Errorf("youtube: %w", err)
		}
		var result struct {
			Items []struct {
				ID struct {
					VideoID string `json:"videoId"`
				} `json:"id"`
				Snippet struct {
					Title        string `json:"title"`
					Description  string `json:"description"`
					ChannelTitle string `json:"channelTitle"`
					PublishedAt  string `json:"publishedAt"`
				} `json:"snippet"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return items, err
		}
		for _, v := range result.Items {
			if v.ID.VideoID == "" || seen[v.ID.VideoID] {
				continue
			}
			seen[v.ID.VideoID] = true
			created, _ := time.Parse(time.RFC3339, v.Snippet.PublishedAt)
			items = append(items, Item{Source: "youtube", ExternalID: v.ID.VideoID, URL: "https://www.youtube.com/watch?v=" + v.ID.VideoID,
				Author: plain(v.Snippet.ChannelTitle), Title: plain(v.Snippet.Title), Text: plain(v.Snippet.Description), Where: "video", Created: created.UTC()})
		}
	}
	return items, nil
}

// VerifyYouTube checks a key with a one-unit request.
func VerifyYouTube(ctx context.Context, baseURL, key string) error {
	if baseURL == "" {
		baseURL = "https://www.googleapis.com/youtube/v3"
	}
	_, err := get(ctx, baseURL+"/videos?part=id&id=jNQXAC9IVRw&key="+url.QueryEscape(key), nil)
	if err != nil && (strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "HTTP 403")) {
		return errors.New("Google did not accept that key, or the YouTube Data API is not enabled for its project")
	}
	return err
}

// ---------------------------------------------------------------- Facebook Pages

// FacebookPage is a Facebook Page the user manages, with a Page access token.
type FacebookPage struct {
	ID    string
	Name  string
	Token string
	Key   string // "facebook:<stored name>", copied to the comments it brings in
}

// Facebook reads the comments on recent posts of the user's own Pages. Meta
// does not let apps search public posts, so this is all Facebook allows.
type Facebook struct {
	BaseURL string // defaults to https://graph.facebook.com (the app's default API version)
	Pages   []FacebookPage
}

func (Facebook) Name() string { return "facebook" }

func (f Facebook) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(f.Pages) == 0 {
		return nil, errors.New("facebook: add a Page in Settings")
	}
	base := f.BaseURL
	if base == "" {
		base = "https://graph.facebook.com"
	}
	var items []Item
	var problems []error
	for _, p := range f.Pages {
		q := url.Values{"fields": {"message,permalink_url,comments.limit(50){id,message,from{name},created_time,permalink_url}"},
			"limit": {"10"}, "access_token": {p.Token}}
		body, err := get(ctx, base+"/"+url.PathEscape(p.ID)+"/posts?"+q.Encode(), nil)
		if err != nil {
			problems = append(problems, fmt.Errorf("facebook: %s: %w", p.Name, err))
			continue
		}
		var result struct {
			Data []struct {
				Message      string `json:"message"`
				PermalinkURL string `json:"permalink_url"`
				Comments     struct {
					Data []struct {
						ID           string `json:"id"`
						Message      string `json:"message"`
						CreatedTime  string `json:"created_time"`
						PermalinkURL string `json:"permalink_url"`
						From         struct {
							Name string `json:"name"`
						} `json:"from"`
					} `json:"data"`
				} `json:"comments"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			problems = append(problems, err)
			continue
		}
		for _, post := range result.Data {
			for _, c := range post.Comments.Data {
				if c.From.Name == p.Name {
					continue // the Page's own replies
				}
				created, _ := time.Parse("2006-01-02T15:04:05-0700", c.CreatedTime)
				items = append(items, Item{Source: "facebook", ExternalID: c.ID, URL: firstNonEmpty(c.PermalinkURL, post.PermalinkURL),
					Author: firstNonEmpty(c.From.Name, "someone"), Text: c.Message, Where: "your Page " + p.Name, Created: created.UTC(),
					ContextText: post.Message, ContextURL: post.PermalinkURL, ContextOwn: true, Account: p.Key})
			}
		}
	}
	return items, errors.Join(problems...)
}

// VerifyFacebookPage checks a Page token and returns the Page's name.
func VerifyFacebookPage(ctx context.Context, baseURL, pageID, token string) (string, error) {
	if baseURL == "" {
		baseURL = "https://graph.facebook.com"
	}
	body, err := get(ctx, baseURL+"/"+url.PathEscape(pageID)+"?fields=name&access_token="+url.QueryEscape(token), nil)
	if err != nil {
		return "", errors.New("Facebook did not accept that Page ID and token; use a Page access token for the Page itself")
	}
	var page struct {
		Name string `json:"name"`
	}
	json.Unmarshal(body, &page)
	return page.Name, nil
}
