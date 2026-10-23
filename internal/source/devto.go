package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DevToAccount is a DEV (dev.to) account: its username and an API key from
// dev.to/settings/extensions.
type DevToAccount struct {
	Username string
	APIKey   string
}

func (a DevToAccount) key() string { return "devto:" + a.Username }

// DevTo reads the comments on each connected account's published articles.
// DEV has no public search API, so it only works with connected accounts.
// It only reads.
type DevTo struct {
	BaseURL  string // defaults to https://dev.to/api
	Accounts []DevToAccount
}

func (DevTo) Name() string { return "devto" }

func devHeaders(key string) map[string]string {
	return map[string]string{"api-key": key, "Accept": "application/vnd.forem.api-v1+json"}
}

type devComment struct {
	IDCode    string                    `json:"id_code"`
	BodyHTML  string                    `json:"body_html"`
	CreatedAt string                    `json:"created_at"`
	User      struct{ Username string } `json:"user"`
	Children  []devComment              `json:"children"`
}

func (d DevTo) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(d.Accounts) == 0 {
		return nil, errors.New("devto: add an account in Settings")
	}
	base := d.BaseURL
	if base == "" {
		base = "https://dev.to/api"
	}
	var items []Item
	var problems []error
	for _, acct := range d.Accounts {
		body, err := get(ctx, base+"/articles/me/published?per_page=30", devHeaders(acct.APIKey))
		if err != nil {
			problems = append(problems, fmt.Errorf("devto: @%s: %w", acct.Username, err))
			continue
		}
		var articles []struct {
			ID            int64  `json:"id"`
			Title         string `json:"title"`
			URL           string `json:"url"`
			CommentsCount int    `json:"comments_count"`
		}
		if err := json.Unmarshal(body, &articles); err != nil {
			problems = append(problems, err)
			continue
		}
		where := "your DEV articles (@" + acct.Username + ")"
		for _, a := range articles {
			if a.CommentsCount == 0 {
				continue
			}
			body, err := get(ctx, base+"/comments?a_id="+url.QueryEscape(fmt.Sprint(a.ID)), devHeaders(acct.APIKey))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			var comments []devComment
			if err := json.Unmarshal(body, &comments); err != nil {
				problems = append(problems, err)
				continue
			}
			var walk func([]devComment)
			walk = func(list []devComment) {
				for _, c := range list {
					walk(c.Children)
					if strings.EqualFold(c.User.Username, acct.Username) {
						continue // the account's own replies
					}
					created, _ := time.Parse(time.RFC3339, c.CreatedAt)
					items = append(items, Item{
						Source: "devto", ExternalID: c.IDCode, URL: "https://dev.to/" + c.User.Username + "/comment/" + c.IDCode,
						Author: c.User.Username, Title: a.Title, Text: devText(c.BodyHTML), Where: where, Created: created.UTC(),
						ContextURL: a.URL, ContextOwn: true, Account: acct.key(),
					})
				}
			}
			walk(comments)
		}
	}
	return items, errors.Join(problems...)
}

// VerifyDevTo checks an API key and returns the username it belongs to.
func VerifyDevTo(ctx context.Context, baseURL, key string) (string, error) {
	if baseURL == "" {
		baseURL = "https://dev.to/api"
	}
	body, err := get(ctx, baseURL+"/users/me", devHeaders(key))
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 401") {
			return "", errors.New("DEV did not accept that API key")
		}
		return "", err
	}
	var user struct {
		Username string `json:"username"`
	}
	json.Unmarshal(body, &user)
	if user.Username == "" {
		return "", errors.New("DEV did not return a username for that key")
	}
	return user.Username, nil
}

var devButtons = regexp.MustCompile(`(?:Enter|Exit) fullscreen mode`)
var blankLines = regexp.MustCompile(`\n{3,}`)

// devText is a comment's text without the labels of DEV's code-block buttons.
func devText(html string) string {
	return strings.TrimSpace(blankLines.ReplaceAllString(devButtons.ReplaceAllString(plain(html), ""), "\n\n"))
}
