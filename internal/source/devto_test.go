package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDevToComments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("api-key") != "k" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/articles/me/published":
			w.Write([]byte(`[{"id":7,"title":"Go JSON v2","url":"https://dev.to/acme/go-json","comments_count":2},{"id":8,"title":"Quiet","comments_count":0}]`))
		case "/comments":
			if r.URL.Query().Get("a_id") != "7" {
				t.Errorf("comments fetched for article %s, which has none", r.URL.Query().Get("a_id"))
			}
			w.Write([]byte(`[{"id_code":"c1","body_html":"<p>Does this work on 1.26?</p><div>Enter fullscreen mode</div><div>Exit fullscreen mode</div>","created_at":"2026-10-10T10:00:00Z","user":{"username":"dana"},
				"children":[{"id_code":"c2","body_html":"<p>Yes</p>","created_at":"2026-10-10T11:00:00Z","user":{"username":"acme"},"children":[]}]}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	items, err := DevTo{BaseURL: srv.URL, Accounts: []DevToAccount{{Username: "acme", APIKey: "k"}}}.Fetch(context.Background(), Watch{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want only dana's comment (the account's own reply is skipped), got %+v", items)
	}
	it := items[0]
	if it.Author != "dana" || it.Text != "Does this work on 1.26?" || it.Title != "Go JSON v2" || it.Where != "your DEV articles (@acme)" {
		t.Errorf("unexpected item: %+v", it)
	}
	if _, err := VerifyDevTo(context.Background(), srv.URL, "wrong"); err == nil || err.Error() != "DEV did not accept that API key" {
		t.Errorf("VerifyDevTo with a bad key = %v", err)
	}
}
