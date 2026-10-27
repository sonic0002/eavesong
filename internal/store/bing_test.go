package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/insight"
)

func TestBingUpgradeKeepsGoogleDataAndFollowThrough(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := s.SaveProject(ctx, Project{Name: "Acme", Domains: []string{"acme.dev"}})
	if err != nil {
		t.Fatal(err)
	}
	// A search that is pushed to page one on either engine.
	report := gsc.Report{Property: "sc-domain:acme.dev", Start: "2026-09-01", End: "2026-09-28", Clicks: 5, Impressions: 300,
		Queries: []gsc.Row{{Key: "acme cli", Impressions: 300, Clicks: 5, CTR: 1.7, Position: 12}}}
	data, _ := json.Marshal(report)
	if err := s.SaveSearchReport(ctx, pid, string(data), ""); err != nil {
		t.Fatal(err)
	}
	recs := insight.Analyze(report)
	if len(recs) == 0 {
		t.Fatal("test report should produce a recommendation")
	}
	googleKey := insight.Key(recs[0])
	if err := s.SetRecommendationAction(ctx, pid, googleKey, "done"); err != nil {
		t.Fatal(err)
	}
	// Make the database look like one from before Bing support.
	if _, err := s.db.Exec(`DROP TABLE bing_reports; DROP TABLE bing_snapshots`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("an older database must open: %v", err)
	}
	defer s.Close()
	saved, err := s.SearchReport(ctx, pid)
	if err != nil || saved.Data != string(data) {
		t.Fatalf("Google report changed by the upgrade: %v, %q", err, saved.Data)
	}
	if b, err := s.BingReport(ctx, pid); err != nil || b.Data != "" {
		t.Fatalf("a new install has no Bing report: %+v, %v", b, err)
	}

	// Bing's report has the same finding; it gets its own key and follow-through.
	report.Engine, report.Property = "bing", "https://acme.dev/"
	bingData, _ := json.Marshal(report)
	if err := s.SaveBingReport(ctx, pid, string(bingData), ""); err != nil {
		t.Fatal(err)
	}
	bingKey := insight.Key(insight.Analyze(report)[0])
	if bingKey != "bing:"+googleKey {
		t.Fatalf("Bing key = %q, want Google's key with a bing: prefix", bingKey)
	}
	if err := s.SetRecommendationAction(ctx, pid, bingKey, "dismissed"); err != nil {
		t.Fatal(err)
	}
	actions := func() map[string]RecommendationAction {
		list, err := s.RecommendationActions(ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]RecommendationAction{}
		for _, a := range list {
			out[a.Key] = a
		}
		return out
	}
	got := actions()
	if got[googleKey].Status != "done" || got[bingKey].Status != "dismissed" {
		t.Fatalf("each engine keeps its own choice: %+v", got)
	}
	if got[googleKey].CheckedAt != "" {
		t.Errorf("a Bing refresh must not re-check Google's recommendations: %+v", got[googleKey])
	}

	// A Google refresh re-checks only Google's.
	if err := s.SaveSearchReport(ctx, pid, string(data), ""); err != nil {
		t.Fatal(err)
	}
	got = actions()
	if got[googleKey].Result != "Still detected" || got[bingKey].CheckedAt != "" {
		t.Errorf("Google refresh: google %+v, bing %+v", got[googleKey], got[bingKey])
	}
	if h, err := s.BingHistory(ctx, pid); err != nil || len(h) != 1 || h[0].Report.Engine != "bing" {
		t.Errorf("Bing history = %+v, %v", h, err)
	}
	if h, err := s.SearchHistory(ctx, pid); err != nil || len(h) != 1 || h[0].Report.Engine != "" {
		t.Errorf("Google history = %+v, %v", h, err)
	}
}
