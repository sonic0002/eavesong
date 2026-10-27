package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProductHunt reads recent launches on Product Hunt through its GraphQL API,
// keeps those whose name, tagline or description mention a term, and reads
// the comments on them. The API has no search, so it pages through every
// launch of the last two days, at most once an hour. It only reads.
type ProductHunt struct {
	BaseURL string // defaults to https://api.producthunt.com/v2/api/graphql
	Tokens  []string
}

func (ProductHunt) Name() string               { return "producthunt" }
func (ProductHunt) MinInterval() time.Duration { return time.Hour }

const (
	phWindow   = 48 * time.Hour
	phMaxPages = 50 // 1,000 launches, well inside the API's complexity limit
)

// phQuery posts a GraphQL query and decodes its data into out.
func phQuery(ctx context.Context, endpoint, token, query string, vars map[string]any, out any) error {
	payload, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	if len(result.Errors) > 0 {
		return errors.New(result.Errors[0].Message)
	}
	return json.Unmarshal(result.Data, out)
}

// phUser is a user name, or "" when Product Hunt hides it (it does for
// everyone but the token's owner).
func phUser(username string) string {
	if strings.Contains(username, "REDACTED") {
		return ""
	}
	return username
}

type phPost struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Tagline       string `json:"tagline"`
	Description   string `json:"description"`
	URL           string `json:"url"`
	CreatedAt     string `json:"createdAt"`
	CommentsCount int    `json:"commentsCount"`
	User          struct {
		Username string `json:"username"`
	} `json:"user"`
}

const phPostsQuery = `query($after: String, $since: DateTime) {
  posts(first: 20, after: $after, order: NEWEST, postedAfter: $since) {
    pageInfo { endCursor hasNextPage }
    edges { node { id name tagline description url createdAt commentsCount user { username } } }
  }
}`

const phCommentsQuery = `query($id: ID!) {
  post(id: $id) {
    comments(first: 50, order: NEWEST) {
      edges { node { id body url createdAt user { username } } }
    }
  }
}`

func (p ProductHunt) Fetch(ctx context.Context, w Watch) ([]Item, error) {
	if len(p.Tokens) == 0 {
		return nil, errors.New("producthunt: add a developer token in Settings")
	}
	endpoint := p.BaseURL
	if endpoint == "" {
		endpoint = "https://api.producthunt.com/v2/api/graphql"
	}
	token := p.Tokens[0]
	since := time.Now().Add(-phWindow).UTC().Format(time.RFC3339)
	terms := w.terms()
	var items []Item
	var after any
	for page := 0; page < phMaxPages; page++ {
		var data struct {
			Posts struct {
				PageInfo struct {
					EndCursor   string `json:"endCursor"`
					HasNextPage bool   `json:"hasNextPage"`
				} `json:"pageInfo"`
				Edges []struct {
					Node phPost `json:"node"`
				} `json:"edges"`
			} `json:"posts"`
		}
		if err := phQuery(ctx, endpoint, token, phPostsQuery, map[string]any{"after": after, "since": since}, &data); err != nil {
			return items, fmt.Errorf("producthunt: %w", err)
		}
		for _, e := range data.Posts.Edges {
			post := e.Node
			var matched []string
			for _, t := range terms {
				if Contains(post.Name+"\n"+post.Tagline+"\n"+post.Description, t) {
					matched = append(matched, t)
				}
			}
			if len(matched) == 0 {
				continue
			}
			created, _ := time.Parse(time.RFC3339, post.CreatedAt)
			items = append(items, Item{
				Source: "producthunt", ExternalID: post.ID, URL: post.URL, Author: phUser(post.User.Username),
				Title: plain(post.Name), Text: plain(post.Tagline + "\n\n" + post.Description), Where: "launch", Created: created.UTC(),
			})
			if post.CommentsCount == 0 {
				continue
			}
			comments, err := p.comments(ctx, endpoint, token, post, matched)
			if err != nil {
				return items, err
			}
			items = append(items, comments...)
		}
		if !data.Posts.PageInfo.HasNextPage || data.Posts.PageInfo.EndCursor == "" {
			break
		}
		after = data.Posts.PageInfo.EndCursor
	}
	return items, nil
}

// comments reads the newest comments on a launch. They carry the launch's
// terms, since a comment rarely repeats the product's name.
func (p ProductHunt) comments(ctx context.Context, endpoint, token string, post phPost, terms []string) ([]Item, error) {
	var data struct {
		Post struct {
			Comments struct {
				Edges []struct {
					Node struct {
						ID        string `json:"id"`
						Body      string `json:"body"`
						URL       string `json:"url"`
						CreatedAt string `json:"createdAt"`
						User      struct {
							Username string `json:"username"`
						} `json:"user"`
					} `json:"node"`
				} `json:"edges"`
			} `json:"comments"`
		} `json:"post"`
	}
	if err := phQuery(ctx, endpoint, token, phCommentsQuery, map[string]any{"id": post.ID}, &data); err != nil {
		return nil, fmt.Errorf("producthunt: comments on %s: %w", post.Name, err)
	}
	var items []Item
	for _, e := range data.Post.Comments.Edges {
		c := e.Node
		created, _ := time.Parse(time.RFC3339, c.CreatedAt)
		link := c.URL
		if link == "" {
			link = post.URL
		}
		items = append(items, Item{
			Source: "producthunt", ExternalID: "comment:" + c.ID, URL: link, Author: phUser(c.User.Username),
			Title: plain(post.Name), Text: plain(c.Body), Where: "comment", Created: created.UTC(),
			Terms: terms, ContextText: plain(post.Tagline), ContextURL: post.URL,
		})
	}
	return items, nil
}

// VerifyProductHunt checks a developer token with one small query.
func VerifyProductHunt(ctx context.Context, endpoint, token string) error {
	if endpoint == "" {
		endpoint = "https://api.producthunt.com/v2/api/graphql"
	}
	var data struct{}
	err := phQuery(ctx, endpoint, token, `{ posts(first: 1) { edges { node { id } } } }`, nil, &data)
	if err != nil && strings.Contains(err.Error(), "HTTP 401") {
		return errors.New("Product Hunt did not accept that token")
	}
	return err
}
