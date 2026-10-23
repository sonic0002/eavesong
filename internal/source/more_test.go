package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serve(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for path, body := range routes {
			if strings.HasPrefix(r.URL.Path, path) {
				w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStackOverflow(t *testing.T) {
	srv := serve(t, map[string]string{"/search/advanced": `{"items":[{"question_id":1,"title":"How do I configure Acme &amp; Go?","body":"<p>Acme fails</p>","link":"https://stackoverflow.com/q/1","creation_date":1760000000,"is_answered":false,"owner":{"display_name":"dana"}}]}`})
	items, err := StackOverflow{BaseURL: srv.URL}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}, Domains: []string{"acme.dev"}})
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, %v (the same question for two terms is kept once)", items, err)
	}
	if items[0].Title != "How do I configure Acme & Go?" || items[0].Where != "question, not answered yet" {
		t.Errorf("unexpected item %+v", items[0])
	}
	if (StackOverflow{}).MinInterval() == 0 || (StackOverflow{Key: "k"}).MinInterval() != 0 {
		t.Error("only keyless Stack Overflow is paced")
	}
}

func TestGoogleAlerts(t *testing.T) {
	srv := serve(t, map[string]string{"/alerts/feeds/": `<feed xmlns="http://www.w3.org/2005/Atom"><title>Google Alert - acme</title>
		<entry><id>tag:google.com,2013:googlealerts/feed:1</id><title>Why we picked &lt;b&gt;Acme&lt;/b&gt;</title><published>2026-10-19T10:00:00Z</published>
		<link href="https://www.google.com/url?rct=j&amp;url=https://blog.example.org/acme-review&amp;ct=ga"/><content>A long review of &lt;b&gt;Acme&lt;/b&gt;</content></entry></feed>`})
	items, err := GoogleAlerts{Feeds: []string{srv.URL + "/alerts/feeds/1/2"}}.Fetch(context.Background(), Watch{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, %v", items, err)
	}
	it := items[0]
	if it.URL != "https://blog.example.org/acme-review" || it.Author != "blog.example.org" || it.Title != "Why we picked Acme" || it.Terms[0] != "acme" {
		t.Errorf("unexpected item %+v", it)
	}
}

func TestMastodon(t *testing.T) {
	srv := serve(t, map[string]string{
		"/api/v1/notifications": `[{"status":{"id":"1","uri":"https://m.example/u/1","url":"https://m.example/@dana/1","content":"<p>@acme does it support Go?</p>","created_at":"2026-10-19T10:00:00Z","account":{"acct":"dana@m.example"}}}]`,
		"/api/v2/search":        `{"statuses":[{"id":"1","uri":"https://m.example/u/1","content":"dup"},{"id":"2","uri":"https://m.example/u/2","url":"https://m.example/@eve/2","content":"<p>Trying Acme today</p>","created_at":"2026-10-19T11:00:00Z","account":{"acct":"eve"}}]}`,
	})
	items, err := Mastodon{Accounts: []MastodonAccount{{Server: srv.URL, Acct: "acme@m.example", Token: "t"}}}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}})
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v, %v (a post found twice is kept once)", items, err)
	}
	if items[0].Where != "your mentions (@acme@m.example)" || items[0].Text != "@acme does it support Go?" || items[1].Author != "eve" {
		t.Errorf("unexpected items %+v", items)
	}
}

type testBudget struct{ left, spent int }

func (b *testBudget) Left(context.Context) int       { return b.left - b.spent }
func (b *testBudget) Spend(_ context.Context, n int) { b.spent += n }

func TestXUsesBudget(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.Query().Get("query")+" max="+r.URL.Query().Get("max_results"))
		w.Write([]byte(`{"data":[{"id":"9","text":"Acme is neat","author_id":"u1","created_at":"2026-10-19T10:00:00Z"}],"includes":{"users":[{"id":"u1","username":"dana"}]}}`))
	}))
	defer srv.Close()

	budget := &testBudget{left: 40}
	items, err := X{BaseURL: srv.URL, Tokens: []string{"b"}, Usernames: []string{"acme"}, Budget: budget}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}, Domains: []string{"acme.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], `"acme.dev" OR "Acme"`) || !strings.Contains(queries[0], "max=40") {
		t.Errorf("queries = %q (one combined search, capped by the budget, then mentions)", queries)
	}
	if budget.spent != 4 || len(items) != 2 || items[0].URL != "https://x.com/dana/status/9" || items[0].Account != "" || items[1].Account != "xaccount:acme" {
		t.Errorf("spent %d, items %+v", budget.spent, items)
	}
	_, err = X{BaseURL: srv.URL, Tokens: []string{"b"}, Budget: &testBudget{left: 5}}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}})
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Errorf("an exhausted budget must stop paid reads, got %v", err)
	}
}

func TestYouTubeAndFacebook(t *testing.T) {
	srv := serve(t, map[string]string{
		"/search": `{"items":[{"id":{"videoId":"abc"},"snippet":{"title":"Acme in 5 minutes","description":"tutorial","channelTitle":"DevTube","publishedAt":"2026-10-19T10:00:00Z"}}]}`,
		"/123/posts": `{"data":[{"message":"We shipped Acme 2","permalink_url":"https://facebook.com/acme/posts/1","comments":{"data":[
			{"id":"c1","message":"Does it run on ARM?","created_time":"2026-10-19T10:00:00+0000","from":{"name":"Dana"}},
			{"id":"c2","message":"Yes it does","created_time":"2026-10-19T11:00:00+0000","from":{"name":"Acme"}}]}}]}`,
	})
	videos, err := YouTube{BaseURL: srv.URL, Keys: []string{"k"}}.Fetch(context.Background(), Watch{Keywords: []string{"Acme"}})
	if err != nil || len(videos) != 1 || videos[0].URL != "https://www.youtube.com/watch?v=abc" || videos[0].Author != "DevTube" {
		t.Errorf("youtube = %+v, %v", videos, err)
	}
	comments, err := Facebook{BaseURL: srv.URL, Pages: []FacebookPage{{ID: "123", Name: "Acme", Token: "t"}}}.Fetch(context.Background(), Watch{})
	if err != nil || len(comments) != 1 {
		t.Fatalf("facebook = %+v, %v (the Page's own reply is skipped)", comments, err)
	}
	if c := comments[0]; c.Author != "Dana" || c.ContextText != "We shipped Acme 2" || c.Where != "your Page Acme" {
		t.Errorf("unexpected comment %+v", c)
	}
}
