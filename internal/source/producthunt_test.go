package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductHunt(t *testing.T) {
	var commentsFor []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch {
		case strings.Contains(req.Query, "comments("):
			commentsFor = append(commentsFor, req.Variables["id"].(string))
			w.Write([]byte(`{"data":{"post":{"comments":{"edges":[{"node":{"id":"c1","body":"<p>Congrats on the launch!</p>","url":"https://www.producthunt.com/posts/acme?comment=c1","createdAt":"2026-10-26T10:00:00Z","user":{"username":"[REDACTED]"}}}]}}}}`))
		case req.Variables["after"] == nil:
			w.Write([]byte(`{"data":{"posts":{"pageInfo":{"endCursor":"p2","hasNextPage":true},"edges":[
				{"node":{"id":"1","name":"Acme","tagline":"Listen for your product","description":"","url":"https://www.producthunt.com/posts/acme","createdAt":"2026-10-26T08:00:00Z","commentsCount":1,"user":{"username":"dana"}}},
				{"node":{"id":"2","name":"Other thing","tagline":"Unrelated","description":"","url":"https://www.producthunt.com/posts/other","createdAt":"2026-10-26T08:00:00Z","commentsCount":5,"user":{"username":"lee"}}}]}}}`))
		default:
			w.Write([]byte(`{"data":{"posts":{"pageInfo":{"endCursor":"","hasNextPage":false},"edges":[
				{"node":{"id":"3","name":"Rival","tagline":"Like Acme, but slower","description":"","url":"https://www.producthunt.com/posts/rival","createdAt":"2026-10-25T08:00:00Z","commentsCount":0,"user":{"username":"[REDACTED]"}}}]}}}`))
		}
	}))
	defer srv.Close()

	items, err := ProductHunt{BaseURL: srv.URL, Tokens: []string{"tok"}}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want the Acme launch, its comment and the Rival launch from page two, got %+v", items)
	}
	if items[0].Title != "Acme" || items[0].Where != "launch" || items[0].Author != "dana" {
		t.Errorf("unexpected launch %+v", items[0])
	}
	c := items[1]
	if c.Where != "comment" || c.Text != "Congrats on the launch!" || c.Author != "" || len(c.Terms) != 1 || c.Terms[0] != "Acme" || c.ContextURL != "https://www.producthunt.com/posts/acme" {
		t.Errorf("unexpected comment %+v", c)
	}
	if len(commentsFor) != 1 || commentsFor[0] != "1" {
		t.Errorf("comments read for %v; want only the matching launch with comments", commentsFor)
	}
	if err := VerifyProductHunt(context.Background(), srv.URL, "wrong"); err == nil || err.Error() != "Product Hunt did not accept that token" {
		t.Errorf("VerifyProductHunt with a bad token = %v", err)
	}
}
