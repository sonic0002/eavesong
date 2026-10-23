package insight

import (
	"strings"
	"testing"

	"github.com/sonic0002/eavesong/internal/gsc"
)

func kinds(recs []Recommendation) map[string]Recommendation {
	out := map[string]Recommendation{}
	for _, r := range recs {
		out[r.Kind+":"+r.Query] = r
	}
	return out
}

func TestAnalyze(t *testing.T) {
	r := gsc.Report{
		Queries: []gsc.Row{
			{Key: "go 1.26 release notes", Clicks: 0, Impressions: 81, CTR: 0, Position: 4.2}, // good position, nobody clicks
			{Key: "rust 1.99", Clicks: 1, Impressions: 117, CTR: 0.9, Position: 9.1},          // page two
			{Key: "scion vcs", Clicks: 5, Impressions: 60, CTR: 8.3, Position: 4.0},           // fine
			{Key: "rust ffi bool abi", Clicks: 0, Impressions: 25, CTR: 0, Position: 38},      // nothing answers it
		},
		QueryPages: []gsc.Row{
			{Key: "rust 1.99", Page: "https://acme.dev/rust/rust-1-99", Impressions: 80},
			{Key: "rust 1.99", Page: "https://acme.dev/rust/rust-1-99-1", Impressions: 37},
			{Key: "go 1.26 release notes", Page: "https://acme.dev/go/go-1-27-upgrade", Impressions: 81},
		},
		Earlier: []gsc.Row{{Key: "scion vcs", Impressions: 0}, {Key: "go 1.27.2", Impressions: 90, Clicks: 3}},
		Recent:  []gsc.Row{{Key: "scion vcs", Impressions: 60}, {Key: "go 1.27.2", Impressions: 12}},
	}
	got := kinds(Analyze(r))
	if miss, ok := got["missing:go 1.26 release notes"]; !ok || !strings.Contains(miss.Detail, "/go/go-1-27-upgrade") || strings.Contains(miss.Detail, "clicks click") {
		t.Errorf("a page about Go 1.27 ranking for a Go 1.26 search needs its own page, got %+v", miss)
	}
	if _, ok := got["title:go 1.26 release notes"]; ok {
		t.Error("rewriting the title of an off-topic page is the wrong advice")
	}
	fix := kinds(Analyze(gsc.Report{
		Queries:    []gsc.Row{{Key: "svn vs scion", Impressions: 32, Clicks: 2, CTR: 6.2, Position: 1.5}, {Key: `site.anthropic.com "scion"`, Impressions: 50, Position: 9}},
		QueryPages: []gsc.Row{{Key: "svn vs scion", Page: "https://acme.dev/ai/scion-vs-svn", Impressions: 32}},
	}))
	if _, ok := fix["title:svn vs scion"]; !ok {
		t.Errorf("an on-topic page clicked far below its position needs a better title, got %v", fix)
	}
	for k := range fix {
		if strings.Contains(k, "site.anthropic.com") {
			t.Errorf("searches with operators are research, not demand: %s", k)
		}
	}
	if push, ok := got["push:rust 1.99"]; !ok || push.Page != "https://acme.dev/rust/rust-1-99" {
		t.Errorf("want a push for rust 1.99 on its best page, got %+v", push)
	}
	if _, ok := got["compete:rust 1.99"]; !ok {
		t.Error("two pages share rust 1.99 and should be flagged")
	}
	if _, ok := got["missing:rust ffi bool abi"]; !ok {
		t.Error("a search shown at position 38 with no clicks needs its own page")
	}
	if _, ok := got["rising:scion vcs"]; !ok {
		t.Error("scion vcs went from 0 to 60 impressions")
	}
	if _, ok := got["falling:go 1.27.2"]; !ok {
		t.Error("go 1.27.2 dropped from 90 to 12 impressions")
	}
	for k := range got {
		if strings.HasSuffix(k, ":scion vcs") && !strings.HasPrefix(k, "rising") {
			t.Errorf("a healthy result should only be flagged as rising, got %s", k)
		}
	}
	if recs := Analyze(gsc.Report{Sitemaps: []gsc.Sitemap{{Path: "/sitemap.xml", Errors: 2}}}); len(recs) != 1 || recs[0].Kind != Sitemap {
		t.Errorf("sitemap errors: %+v", recs)
	}
}
