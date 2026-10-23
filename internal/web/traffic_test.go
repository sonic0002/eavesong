package web

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/ga"
	"github.com/sonic0002/eavesong/internal/monitor"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
)

func TestTrafficTabShowsVisitsNextToMentions(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pid, err := db.SaveProject(ctx, store.Project{Name: "Acme", Domains: []string{"acme.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, src := range []string{"hackernews", "hackernews", "reddit"} {
		if _, err := db.AddMention(ctx, store.Mention{ProjectID: pid, Source: src, ExternalID: string(rune('a' + i)), URL: "https://example.org/" + src, Title: "acme", PostedAt: time.Now(), About: "product"}); err != nil {
			t.Fatal(err)
		}
	}
	today := time.Now().UTC().Format("2006-01-02")
	report := ga.Report{Property: "properties/2", Name: "Acme site", Start: time.Now().UTC().AddDate(0, 0, -27).Format("2006-01-02"), End: today,
		Users: 120, Sessions: 150, Views: 400, Engaged: 62, AvgSeconds: 75, PrevUsers: 100, PrevSessions: 100, PrevViews: 400,
		Days:    []ga.Row{{Key: time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02"), Users: 4, Sessions: 5, Views: 9}, {Key: today, Users: 9, Sessions: 10, Views: 20}},
		Sources: []ga.Row{{Key: "news.ycombinator.com", Medium: "referral", Sessions: 40}, {Key: "old.reddit.com", Medium: "referral", Sessions: 7}, {Key: "google", Medium: "organic", Sessions: 60}},
		Pages:   []ga.Row{{Key: "/docs/", Views: 90, Users: 50}}}
	data, _ := json.Marshal(report)
	if err := db.SaveAnalyticsReport(ctx, pid, string(data), ""); err != nil {
		t.Fatal(err)
	}

	// A connected key; the saved report is fresh, so opening the tab must not fetch.
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	key, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "r@x.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": "http://127.0.0.1:1/token"})
	client, err := ga.New(string(key))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &monitor.Monitor{Store: db, Log: log}
	m.ConfigureAnalytics([]*ga.Client{client})
	box, _ := secret.New(make([]byte, 32))
	s := &Server{Store: db, Monitor: m, Config: config.Manager{Store: db, Box: box}, Log: log}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/p/"+itoa(pid)+"/traffic", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d: %.500s", rec.Code, body)
	}
	if n := strings.Count(body, `class="day-bar"`); n != 28 {
		t.Errorf("chart has %d day bars, want one for each of the 28 days", n)
	}
	for _, want := range []string{"Visitors per day", "up 20% on the 28 days before", "day-bar", "From the places Eavesong listens", "news.ycombinator.com", "/docs/"} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	// Hacker News: 40 visits and 2 mentions; Reddit: 7 visits and 1 mention.
	hn := strings.Index(body, ">Hacker News</td>")
	if hn < 0 {
		hn = strings.Index(body, "Hacker News</td>")
	}
	if hn < 0 || !strings.Contains(body[hn:min(len(body), hn+120)], `<td class="num">40</td><td class="num">2</td>`) {
		t.Errorf("Hacker News row should show 40 visits and 2 mentions; got %.200s", body[max(0, hn):min(len(body), max(0, hn)+200)])
	}
	if m.AnalyticsFetching(pid) {
		t.Error("a fresh report must not start a fetch")
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestFailureNumbersTheSteps(t *testing.T) {
	got := string(failure("none of the keys can read it\nOpen **Search Console**\nAdd `a@b.c` with **Restricted** permission <now>"))
	want := `<p class="fail-problem">None of the keys can read it.</p><ol class="fix-steps"><li>Open <strong>Search Console</strong>.</li><li>Add <code>a@b.c</code> with <strong>Restricted</strong> permission &lt;now&gt;.</li></ol>`
	if got != want {
		t.Errorf("failure =\n%s\nwant\n%s", got, want)
	}
	if got := string(failure("quota exceeded")); got != `<p class="fail-problem">Quota exceeded.</p>` {
		t.Errorf("one-line error: %s", got)
	}
}

func TestErrorShownOnceWithoutData(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pid, _ := db.SaveProject(ctx, store.Project{Name: "Acme", Domains: []string{"acme.dev"}})
	problem := "none of the connected keys can read it\nOpen the property\nAdd the account"
	db.SaveAnalyticsReport(ctx, pid, "", problem)
	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	key, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "r@x.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": "http://127.0.0.1:1/token"})
	client, _ := ga.New(string(key))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := &monitor.Monitor{Store: db, Log: log}
	m.ConfigureAnalytics([]*ga.Client{client})
	box, _ := secret.New(make([]byte, 32))
	s := &Server{Store: db, Monitor: m, Config: config.Manager{Store: db, Box: box}, Log: log}
	rec := httptest.NewRecorder()
	// The page a failed Refresh redirects to: the error is both in the URL and saved.
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/p/"+itoa(pid)+"/traffic?error="+url.QueryEscape(problem), nil))
	if n := strings.Count(rec.Body.String(), "None of the connected keys can read it."); n != 1 {
		t.Errorf("the error appears %d times, want once", n)
	}
}
