package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestContains(t *testing.T) {
	cases := []struct {
		text, term string
		want       bool
	}{
		{"I tried Eavesong yesterday", "eavesong", true},
		{"see https://acme.dev/docs", "acme.dev", true},
		{"go is fine", "go", true},
		{"ask google", "go", false},  // not inside another word
		{"golang tips", "go", false}, // not a prefix of a word
		{"acme.devices", "acme.dev", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := Contains(c.text, c.term); got != c.want {
			t.Errorf("Contains(%q, %q) = %v, want %v", c.text, c.term, got, c.want)
		}
	}
}

func TestHackerNews(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search_by_date" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"hits":[
			{"objectID":"1","author":"ann","title":"Show HN: Acme","url":"https://acme.dev","created_at_i":1760000000},
			{"objectID":"2","author":"bob","comment_text":"I use <i>acme.dev</i> &amp; like it","story_title":"Tools","created_at_i":1760000100}
		]}`))
	}))
	defer srv.Close()

	items, err := HackerNews{BaseURL: srv.URL}.Fetch(context.Background(), Watch{Domains: []string{"acme.dev"}, Keywords: []string{"Acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 { // the same hits come back for both terms and are kept once
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[1].Where != "comment" || items[1].Text != "I use acme.dev & like it" || items[1].Title != "Tools" {
		t.Errorf("comment parsed wrong: %+v", items[1])
	}
	if items[0].URL != "https://news.ycombinator.com/item?id=1" {
		t.Errorf("story URL = %q", items[0].URL)
	}
}

func TestRedditDomainFeedMarksTerm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><entry>
			<id>t3_abc</id><title>A link post</title><published>2026-10-01T10:00:00+00:00</published>
			<author><name>/u/carol</name></author><category term="golang"/>
			<link href="https://www.reddit.com/r/golang/comments/abc/"/><content type="html">&lt;a href="https://acme.dev/x"&gt;[link]&lt;/a&gt;</content>
		</entry></feed>`))
	}))
	defer srv.Close()

	items, err := Reddit{BaseURL: srv.URL, Pause: 1}.Fetch(context.Background(), Watch{Domains: []string{"acme.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Author != "carol" || items[0].Where != "r/golang" {
		t.Fatalf("unexpected items: %+v", items)
	}
	if len(items[0].Terms) != 1 || items[0].Terms[0] != "acme.dev" {
		t.Errorf("domain feed item should carry the domain as a matched term, got %v", items[0].Terms)
	}
}

func TestBlueskyReplyContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/com.atproto.server.createSession":
			if r.Header.Get("X-Eavesong-Did") != "" {
				t.Error("internal values must not be sent to Bluesky")
			}
			w.Write([]byte(`{"accessJwt":"jwt","handle":"acme.dev","did":"did:plc:me"}`))
		case "/xrpc/app.bsky.notification.listNotifications":
			w.Write([]byte(`{"notifications":[
				{"uri":"at://did:plc:x/app.bsky.feed.post/r1","reason":"reply","author":{"handle":"dana.bsky.social"},
				 "record":{"text":"Useful gotcha","createdAt":"2026-10-20T01:00:00Z","reply":{"parent":{"uri":"at://did:plc:me/app.bsky.feed.post/p1"}}}},
				{"uri":"at://did:plc:y/app.bsky.feed.post/l1","reason":"like","author":{"handle":"eve.bsky.social"},"record":{}}
			]}`))
		case "/xrpc/app.bsky.feed.getPosts":
			if r.URL.Query().Get("uris") != "at://did:plc:me/app.bsky.feed.post/p1" {
				t.Errorf("asked for %v", r.URL.Query()["uris"])
			}
			w.Write([]byte(`{"posts":[{"uri":"at://did:plc:me/app.bsky.feed.post/p1","author":{"handle":"acme.dev"},"record":{"text":"Claude Code skips AGENTS.md if any CLAUDE.md sits above your repo"}}]}`))
		case "/xrpc/app.bsky.feed.searchPosts":
			w.Write([]byte(`{"posts":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	items, err := Bluesky{Service: srv.URL, Accounts: []BlueskyAccount{{Handle: "did:plc:me", AppPassword: "p"}}}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want the reply only (likes are not mentions), got %+v", items)
	}
	it := items[0]
	if !it.ContextOwn {
		t.Error("a reply to a post in the signed-in account's repository answers the team's own post")
	}
	if it.ContextText != "Claude Code skips AGENTS.md if any CLAUDE.md sits above your repo" || it.ContextURL != "https://bsky.app/profile/acme.dev/post/p1" {
		t.Errorf("reply context = %q, %q", it.ContextText, it.ContextURL)
	}
}
