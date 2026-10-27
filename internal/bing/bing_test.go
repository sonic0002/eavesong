package bing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wcf writes a date the way Bing does, days ago from now.
func wcf(daysAgo int) string {
	return fmt.Sprintf(`"\/Date(%d-0700)\/"`, time.Now().UTC().AddDate(0, 0, -daysAgo).UnixMilli())
}

func TestFetchReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("apikey") != "k" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"ErrorCode":3,"Message":"InvalidApiKey"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/GetUserSites") {
			w.Write([]byte(`{"d":[{"Url":"https://www.acme.dev/","IsVerified":true},{"Url":"https://other.org/","IsVerified":false}]}`))
			return
		}
		if r.URL.Query().Get("siteUrl") != "https://www.acme.dev/" {
			t.Errorf("%s asked for site %q", r.URL.Path, r.URL.Query().Get("siteUrl"))
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/GetRankAndTrafficStats"):
			w.Write([]byte(`{"d":[{"Date":` + wcf(5) + `,"Clicks":3,"Impressions":100},{"Date":` + wcf(4) + `,"Clicks":5,"Impressions":140},{"Date":` + wcf(90) + `,"Clicks":50,"Impressions":900}]}`))
		case strings.HasSuffix(r.URL.Path, "/GetQueryStats"):
			w.Write([]byte(`{"d":[
				{"Query":"acme cli","Date":` + wcf(20) + `,"Clicks":1,"Impressions":30,"AvgImpressionPosition":4},
				{"Query":"acme cli","Date":` + wcf(6) + `,"Clicks":3,"Impressions":90,"AvgImpressionPosition":2},
				{"Query":"acme vs rival","Date":` + wcf(6) + `,"Clicks":0,"Impressions":40,"AvgImpressionPosition":11.4},
				{"Query":"old news","Date":` + wcf(120) + `,"Clicks":9,"Impressions":400,"AvgImpressionPosition":1}]}`))
		case strings.HasSuffix(r.URL.Path, "/GetPageStats"):
			w.Write([]byte(`{"d":[{"Query":"https://www.acme.dev/docs","Date":` + wcf(6) + `,"Clicks":8,"Impressions":240,"AvgImpressionPosition":-1}]}`))
		case strings.HasSuffix(r.URL.Path, "/GetFeeds"):
			w.Write([]byte(`{"d":[{"Url":"https://www.acme.dev/sitemap.xml","Status":"Error","LastCrawled":` + wcf(3) + `}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c := New("k")
	c.APIBase = srv.URL
	sites, err := c.Sites(ctx)
	if err != nil || len(sites) != 1 {
		t.Fatalf("sites = %v, %v (unverified sites are left out)", sites, err)
	}
	site := SiteFor(sites, []string{"acme.dev"})
	if site != "https://www.acme.dev/" {
		t.Fatalf("SiteFor = %q", site)
	}
	r, err := c.Fetch(ctx, site)
	if err != nil {
		t.Fatal(err)
	}
	if r.Engine != "bing" || r.Clicks != 8 || r.Impressions != 240 || len(r.Days) != 2 {
		t.Errorf("totals should count only the last 28 days: %+v", r)
	}
	if len(r.Queries) != 2 || r.Queries[0].Key != "acme cli" || r.Queries[0].Impressions != 120 || r.Queries[0].Clicks != 4 {
		t.Fatalf("queries should be summed per search within the period: %+v", r.Queries)
	}
	if p := r.Queries[0].Position; p < 2.49 || p > 2.51 {
		t.Errorf("position should be weighted by impressions: got %.2f, want 2.5", p)
	}
	if len(r.Earlier) != 1 || len(r.Recent) != 2 {
		t.Errorf("halves: earlier %+v, recent %+v", r.Earlier, r.Recent)
	}
	if len(r.PageTwo) != 1 || r.PageTwo[0].Key != "acme vs rival" {
		t.Errorf("page two = %+v", r.PageTwo)
	}
	if len(r.Pages) != 1 || r.Pages[0].Position != 0 {
		t.Errorf("a page without a position keeps none: %+v", r.Pages)
	}
	if len(r.Sitemaps) != 1 || r.Sitemaps[0].Errors != 1 || r.Sitemaps[0].Status != "Error" {
		t.Errorf("sitemaps = %+v", r.Sitemaps)
	}

	_, err = Verify(ctx, srv.URL, "wrong")
	if err == nil || err.Error() != "Bing did not accept that API key" {
		t.Errorf("Verify with a bad key = %v", err)
	}
	bad := New("secret-key")
	bad.APIBase = "http://127.0.0.1:1"
	if _, err := bad.Sites(ctx); err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("errors must not reveal the key: %v", err)
	}
}
