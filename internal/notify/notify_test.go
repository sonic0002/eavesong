package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAttentionWebhookDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := config.Manager{Store: db, Box: box}
	if err := manager.Save(ctx, config.Settings{AIProvider: "none", SlackWebhook: "https://example.test/hook", NotifyAttention: "1"}); err != nil {
		t.Fatal(err)
	}
	pid, err := db.SaveProject(ctx, store.Project{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddMention(ctx, store.Mention{ProjectID: pid, Source: "reddit", ExternalID: "one", URL: "https://example.com/post", PostedAt: time.Now(), Title: "Problem"}); err != nil {
		t.Fatal(err)
	}
	mentions, err := db.Mentions(ctx, pid, store.Filter{})
	if err != nil || len(mentions) != 1 {
		t.Fatalf("mentions: %v %d", err, len(mentions))
	}
	if err := db.SetDraft(ctx, mentions[0].ID, "attention", "", "Needs review"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://example.test/hook" {
			t.Errorf("URL %s", r.URL)
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "Problem") {
			t.Errorf("payload %s", b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})}
	service := Service{Store: db, Config: manager, Client: client}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("sent %d times, want once", calls)
	}
}

func TestWeeklyDigestCombinesMentionsAndSearch(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "report.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := store.Project{Name: "Acme", Keywords: []string{"Acme"}, Competitors: []string{"Rival"}}
	p.ID, err = db.SaveProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 26, 9, 0, 0, 0, time.UTC)
	for _, m := range []store.Mention{
		{ProjectID: p.ID, Source: "reddit", ExternalID: "one", Title: "Acme mentioned", PostedAt: now.Add(-24 * time.Hour), Terms: []string{"Acme"}},
		{ProjectID: p.ID, Source: "hackernews", ExternalID: "two", Title: "Rival mentioned", PostedAt: now.Add(-48 * time.Hour), Terms: []string{"Rival"}},
		{ProjectID: p.ID, Source: "reddit", ExternalID: "old", PostedAt: now.Add(-8 * 24 * time.Hour), Terms: []string{"Acme"}},
	} {
		if _, err := db.AddMention(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	report, _ := json.Marshal(gsc.Report{Start: "2026-10-19", End: "2026-10-25", Clicks: 42, Impressions: 600})
	if err := db.SaveSearchReport(ctx, p.ID, string(report), ""); err != nil {
		t.Fatal(err)
	}
	event, err := (&Service{Store: db}).digest(ctx, p, 7, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"2 mentions", "reddit: 1", "hackernews: 1", "Acme: 1", "Rival: 1", "42 clicks", "600 impressions"} {
		if !strings.Contains(event.Text, want) {
			t.Errorf("weekly digest missing %q: %s", want, event.Text)
		}
	}
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	manager := config.Manager{Store: db, Box: box}
	if err := manager.Save(ctx, config.Settings{AIProvider: "none", SlackWebhook: "https://example.test/hook", NotifyWeekly: "1"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "42 clicks") {
			t.Errorf("weekly webhook missing report: %s", body)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})}
	service := Service{Store: db, Config: manager, Client: client, Now: func() time.Time { return now }}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("weekly report sent %d times, want once", calls)
	}
}
