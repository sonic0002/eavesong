// Package bing reads search data from the Bing Webmaster Tools API with the
// API key a user creates under Settings, API access. It returns the same
// report shape as Search Console, so the Search tab and its recommendations
// work the same way for both engines.
package bing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/gsc"
)

// Client reads one Bing Webmaster Tools account. The key belongs to a user,
// not a site, and reads every site that user has verified.
type Client struct {
	Key     string
	APIBase string // defaults to https://ssl.bing.com/webmaster/api.svc/json
	http    *http.Client
}

func New(key string) *Client {
	return &Client{Key: key, http: &http.Client{Timeout: 30 * time.Second}}
}

// call runs one API method and decodes the "d" field of its answer. Errors
// never include the request URL, which carries the key.
func (c *Client) call(ctx context.Context, method string, query url.Values, out any) error {
	base := c.APIBase
	if base == "" {
		base = "https://ssl.bing.com/webmaster/api.svc/json"
	}
	if query == nil {
		query = url.Values{}
	}
	query.Set("apikey", c.Key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+method+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	client := c.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // drop the URL and its key
		}
		return fmt.Errorf("bing %s: %w", method, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var problem struct {
			ErrorCode int
			Message   string
		}
		json.Unmarshal(body, &problem)
		if problem.Message != "" {
			return fmt.Errorf("bing %s: HTTP %d: %s", method, resp.StatusCode, problem.Message)
		}
		return fmt.Errorf("bing %s: HTTP %d", method, resp.StatusCode)
	}
	var wrapped struct {
		D json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return fmt.Errorf("bing %s: %w", method, err)
	}
	if len(wrapped.D) == 0 || string(wrapped.D) == "null" {
		return nil
	}
	return json.Unmarshal(wrapped.D, out)
}

// Sites lists the verified sites the key can read, sorted.
func (c *Client) Sites(ctx context.Context) ([]string, error) {
	var sites []struct {
		URL        string `json:"Url"`
		IsVerified bool   `json:"IsVerified"`
	}
	if err := c.call(ctx, "GetUserSites", nil, &sites); err != nil {
		return nil, err
	}
	var out []string
	for _, s := range sites {
		if s.IsVerified {
			out = append(out, s.URL)
		}
	}
	sort.Strings(out)
	return out, nil
}

// SiteFor picks the site that covers one of a project's domains.
func SiteFor(sites, domains []string) string {
	have := map[string]string{}
	for _, s := range sites {
		have[strings.ToLower(strings.TrimSuffix(s, "/"))+"/"] = s
	}
	for _, d := range domains {
		d = strings.TrimPrefix(strings.ToLower(d), "www.")
		for _, candidate := range []string{"https://" + d + "/", "https://www." + d + "/", "http://" + d + "/", "http://www." + d + "/"} {
			if s, ok := have[candidate]; ok {
				return s
			}
		}
	}
	return ""
}

var wcfDate = regexp.MustCompile(`/Date\((-?\d+)`)

// day turns Bing's "/Date(1760000000000-0700)/" into "2006-01-02" (UTC).
func day(s string) string {
	m := wcfDate.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	ms, _ := strconv.ParseInt(m[1], 10, 64)
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}

type stat struct {
	Query                 string  `json:"Query"`
	Date                  string  `json:"Date"`
	Clicks                int     `json:"Clicks"`
	Impressions           int     `json:"Impressions"`
	AvgImpressionPosition float64 `json:"AvgImpressionPosition"`
}

// sum adds up the rows dated from start to end (inclusive) for each query or
// page, with the position weighted by impressions, most shown first.
func sum(stats []stat, start, end string) []gsc.Row {
	type acc struct {
		row    gsc.Row
		weight float64
		posSum float64
	}
	by := map[string]*acc{}
	var order []string
	for _, s := range stats {
		d := day(s.Date)
		if s.Query == "" || d < start || d > end {
			continue
		}
		a := by[s.Query]
		if a == nil {
			a = &acc{row: gsc.Row{Key: s.Query}}
			by[s.Query] = a
			order = append(order, s.Query)
		}
		a.row.Clicks += s.Clicks
		a.row.Impressions += s.Impressions
		if s.AvgImpressionPosition > 0 && s.Impressions > 0 { // Bing sends -1 when it has no position
			a.posSum += s.AvgImpressionPosition * float64(s.Impressions)
			a.weight += float64(s.Impressions)
		}
	}
	rows := make([]gsc.Row, 0, len(order))
	for _, k := range order {
		a := by[k]
		if a.weight > 0 {
			a.row.Position = a.posSum / a.weight
		}
		if a.row.Impressions > 0 {
			a.row.CTR = float64(a.row.Clicks) / float64(a.row.Impressions) * 100
		}
		rows = append(rows, a.row)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Impressions > rows[j].Impressions })
	return rows
}

// Fetch builds the last 28 days of Bing search data for a site, in the same
// shape as a Search Console report. Bing groups searches and pages by week,
// so the halves used for rising and falling searches are approximate, and it
// has no "which page ranks for this search" breakdown in one call, so the
// recommendations that need one are not made.
func (c *Client) Fetch(ctx context.Context, site string) (gsc.Report, error) {
	end := time.Now().UTC().AddDate(0, 0, -2)
	start := end.AddDate(0, 0, -27)
	r := gsc.Report{Engine: "bing", Property: site, Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), FetchedAt: time.Now().UTC()}
	q := url.Values{"siteUrl": {site}}

	var days []struct {
		Date        string `json:"Date"`
		Clicks      int    `json:"Clicks"`
		Impressions int    `json:"Impressions"`
	}
	if err := c.call(ctx, "GetRankAndTrafficStats", q, &days); err != nil {
		return r, err
	}
	for _, d := range days {
		key := day(d.Date)
		if key < r.Start || key > r.End {
			continue
		}
		r.Days = append(r.Days, gsc.Row{Key: key, Clicks: d.Clicks, Impressions: d.Impressions})
		r.Clicks += d.Clicks
		r.Impressions += d.Impressions
	}
	sort.Slice(r.Days, func(i, j int) bool { return r.Days[i].Key < r.Days[j].Key })

	var queries, pages []stat
	if err := c.call(ctx, "GetQueryStats", q, &queries); err != nil {
		return r, err
	}
	if err := c.call(ctx, "GetPageStats", q, &pages); err != nil {
		return r, err
	}
	r.Queries = sum(queries, r.Start, r.End)
	r.Pages = sum(pages, r.Start, r.End)
	middle := start.AddDate(0, 0, 14)
	r.Earlier = sum(queries, r.Start, middle.AddDate(0, 0, -1).Format("2006-01-02"))
	r.Recent = sum(queries, middle.Format("2006-01-02"), r.End)
	for _, row := range r.Queries {
		if row.Position >= 8 && row.Position <= 20 && row.Impressions >= 5 {
			r.PageTwo = append(r.PageTwo, row)
		}
	}

	var feeds []struct {
		URL         string `json:"Url"`
		Status      string `json:"Status"`
		LastCrawled string `json:"LastCrawled"`
	}
	if c.call(ctx, "GetFeeds", q, &feeds) == nil {
		for _, f := range feeds {
			m := gsc.Sitemap{Path: f.URL, LastDownloaded: day(f.LastCrawled), Status: f.Status}
			if s := strings.ToLower(f.Status); strings.Contains(s, "error") || strings.Contains(s, "fail") {
				m.Errors = 1 // Bing reports a state, not a count
			}
			r.Sitemaps = append(r.Sitemaps, m)
		}
	}
	return r, nil
}

// Verify checks a key and lists the sites it can read.
func Verify(ctx context.Context, apiBase, key string) ([]string, error) {
	c := New(key)
	c.APIBase = apiBase
	sites, err := c.Sites(ctx)
	if err != nil && (strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "HTTP 403")) {
		return nil, errors.New("Bing did not accept that API key")
	}
	return sites, err
}
