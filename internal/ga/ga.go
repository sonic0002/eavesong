// Package ga reads Google Analytics 4 data with a service account that the
// site owner added to their property as a Viewer. It only reads.
package ga

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/gsc"
)

const scope = "https://www.googleapis.com/auth/analytics.readonly"

// Client calls the Analytics Admin API (to find properties) and the Analytics
// Data API (to read reports) as one service account.
type Client struct {
	token     *gsc.Token
	http      *http.Client
	AdminBase string // defaults to https://analyticsadmin.googleapis.com
	DataBase  string // defaults to https://analyticsdata.googleapis.com
}

func New(keyJSON string) (*Client, error) {
	t, err := gsc.NewToken(keyJSON, scope)
	if err != nil {
		return nil, err
	}
	return &Client{token: t, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *Client) Email() string { return c.token.Email() }

func (c *Client) call(ctx context.Context, method, base, path string, body, out any) error {
	token, err := c.token.Get(ctx)
	if err != nil {
		return err
	}
	var payload io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		payload = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Google Analytics: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return explain(resp.StatusCode, data, c.Email())
	}
	return json.Unmarshal(data, out)
}

// explain turns Google's error into what the owner has to do.
func explain(status int, body []byte, email string) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e)
	text := string(body)
	switch {
	case strings.Contains(text, "SERVICE_DISABLED") || strings.Contains(text, "has not been used in project"):
		return fmt.Errorf("the Google Analytics APIs are not enabled for this key's Google Cloud project\n"+
			"Open the **Google Cloud console** and select the project `%s` belongs to (the part after the @)\n"+
			"Go to **APIs & Services**, then **Library**\n"+
			"Enable the **Google Analytics Admin API** and the **Google Analytics Data API**\n"+
			"Wait a minute for Google to apply it, then try again", email)
	case status == http.StatusForbidden:
		return fmt.Errorf("%s has no access to this Google Analytics property\n"+
			"Open **Google Analytics** and select the property\n"+
			"Go to **Admin**, then **Property access management**, and add a user\n"+
			"Add `%s` with the **Viewer** role", email, email)
	case e.Error.Message != "":
		return fmt.Errorf("Google Analytics: %s", e.Error.Message)
	}
	return fmt.Errorf("Google Analytics: HTTP %d: %.200s", status, body)
}

// Property is one Analytics property the service account can read.
type Property struct {
	ID    string   // "properties/123456"
	Name  string   // display name
	Hosts []string // hosts of its web data streams, such as "example.com"
}

// Properties lists the properties the service account can read, with the
// websites their data streams measure.
func (c *Client) Properties(ctx context.Context) ([]Property, error) {
	admin := c.AdminBase
	if admin == "" {
		admin = "https://analyticsadmin.googleapis.com"
	}
	var props []Property
	pageToken := ""
	for {
		var out struct {
			AccountSummaries []struct {
				PropertySummaries []struct {
					Property    string `json:"property"`
					DisplayName string `json:"displayName"`
				} `json:"propertySummaries"`
			} `json:"accountSummaries"`
			NextPageToken string `json:"nextPageToken"`
		}
		path := "/v1beta/accountSummaries?pageSize=200"
		if pageToken != "" {
			path += "&pageToken=" + url.QueryEscape(pageToken)
		}
		if err := c.call(ctx, http.MethodGet, admin, path, nil, &out); err != nil {
			return nil, err
		}
		for _, a := range out.AccountSummaries {
			for _, p := range a.PropertySummaries {
				props = append(props, Property{ID: p.Property, Name: p.DisplayName})
			}
		}
		if pageToken = out.NextPageToken; pageToken == "" {
			break
		}
	}
	for i := range props {
		var streams struct {
			DataStreams []struct {
				WebStreamData struct {
					DefaultURI string `json:"defaultUri"`
				} `json:"webStreamData"`
			} `json:"dataStreams"`
		}
		if err := c.call(ctx, http.MethodGet, admin, "/v1beta/"+props[i].ID+"/dataStreams", nil, &streams); err != nil {
			return nil, err
		}
		for _, s := range streams.DataStreams {
			if h := host(s.WebStreamData.DefaultURI); h != "" {
				props[i].Hosts = append(props[i].Hosts, h)
			}
		}
	}
	sort.Slice(props, func(i, j int) bool { return props[i].Name < props[j].Name })
	return props, nil
}

// host reduces a URL or domain to its host without "www.".
func host(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// PropertyFor picks the property whose web stream measures one of the
// project's domains, or failing that, whose name contains one.
func PropertyFor(props []Property, domains []string) (Property, bool) {
	for _, d := range domains {
		d = host(d)
		for _, p := range props {
			for _, h := range p.Hosts {
				if h == d {
					return p, true
				}
			}
		}
	}
	for _, d := range domains {
		d = host(d)
		for _, p := range props {
			if d != "" && strings.Contains(strings.ToLower(p.Name), d) {
				return p, true
			}
		}
	}
	return Property{}, false
}

// Row is one line of a breakdown.
type Row struct {
	Key      string  `json:"key"`
	Medium   string  `json:"medium,omitempty"` // for sources: referral, organic, (none)...
	Users    int     `json:"users"`
	Sessions int     `json:"sessions"`
	Views    int     `json:"views"`
	Engaged  float64 `json:"engaged"` // engagement rate, percent
}

// Report is what the Traffic tab shows: the last 28 complete days.
type Report struct {
	Property  string    `json:"property"`
	Name      string    `json:"name"`
	Start     string    `json:"start"`
	End       string    `json:"end"`
	FetchedAt time.Time `json:"fetched_at"`

	Users      int     `json:"users"`
	Sessions   int     `json:"sessions"`
	Views      int     `json:"views"`
	Engaged    float64 `json:"engaged"`     // engagement rate, percent
	AvgSeconds float64 `json:"avg_seconds"` // average session duration

	// The 28 days before, for comparison.
	PrevUsers    int `json:"prev_users"`
	PrevSessions int `json:"prev_sessions"`
	PrevViews    int `json:"prev_views"`

	Days     []Row `json:"days"`
	Channels []Row `json:"channels"`
	Sources  []Row `json:"sources"`
	Landing  []Row `json:"landing"`
	Pages    []Row `json:"pages"`
}

type dim struct {
	Name string `json:"name"`
}
type metric struct {
	Name string `json:"name"`
}

// run calls runReport and returns each row as its dimension values and its
// metric values by name.
func (c *Client) run(ctx context.Context, property string, ranges [][2]string, dims, metrics []string, orderBy string, limit int) ([]resultRow, error) {
	data := c.DataBase
	if data == "" {
		data = "https://analyticsdata.googleapis.com"
	}
	body := map[string]any{"limit": limit}
	var dr []map[string]string
	for _, r := range ranges {
		dr = append(dr, map[string]string{"startDate": r[0], "endDate": r[1]})
	}
	body["dateRanges"] = dr
	var ds []dim
	for _, d := range dims {
		ds = append(ds, dim{d})
	}
	if len(ds) > 0 {
		body["dimensions"] = ds
	}
	var ms []metric
	for _, m := range metrics {
		ms = append(ms, metric{m})
	}
	body["metrics"] = ms
	switch {
	case orderBy == "date":
		body["orderBys"] = []any{map[string]any{"dimension": map[string]string{"dimensionName": "date"}}}
	case orderBy != "":
		body["orderBys"] = []any{map[string]any{"metric": map[string]string{"metricName": orderBy}, "desc": true}}
	}
	var out struct {
		DimensionHeaders []dim    `json:"dimensionHeaders"`
		MetricHeaders    []metric `json:"metricHeaders"`
		Rows             []struct {
			DimensionValues []struct {
				Value string `json:"value"`
			} `json:"dimensionValues"`
			MetricValues []struct {
				Value string `json:"value"`
			} `json:"metricValues"`
		} `json:"rows"`
	}
	if err := c.call(ctx, http.MethodPost, data, "/v1beta/"+property+":runReport", body, &out); err != nil {
		return nil, err
	}
	rows := make([]resultRow, 0, len(out.Rows))
	for _, r := range out.Rows {
		row := resultRow{dims: map[string]string{}, metrics: map[string]float64{}}
		for i, v := range r.DimensionValues {
			if i < len(out.DimensionHeaders) {
				row.dims[out.DimensionHeaders[i].Name] = v.Value
			}
		}
		for i, v := range r.MetricValues {
			if i < len(out.MetricHeaders) {
				f, _ := strconv.ParseFloat(v.Value, 64)
				row.metrics[out.MetricHeaders[i].Name] = f
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type resultRow struct {
	dims    map[string]string
	metrics map[string]float64
}

func (r resultRow) toRow(key string) Row {
	return Row{Key: key, Users: int(r.metrics["activeUsers"]), Sessions: int(r.metrics["sessions"]),
		Views: int(r.metrics["screenPageViews"]), Engaged: r.metrics["engagementRate"] * 100}
}

// Fetch reads the last 28 complete days (ending yesterday) for a property.
func (c *Client) Fetch(ctx context.Context, p Property) (Report, error) {
	end := time.Now().UTC().AddDate(0, 0, -1)
	start := end.AddDate(0, 0, -27)
	prevEnd := start.AddDate(0, 0, -1)
	prevStart := prevEnd.AddDate(0, 0, -27)
	day := func(t time.Time) string { return t.Format("2006-01-02") }
	r := Report{Property: p.ID, Name: p.Name, Start: day(start), End: day(end), FetchedAt: time.Now().UTC()}
	now := [][2]string{{r.Start, r.End}}

	totals, err := c.run(ctx, p.ID, [][2]string{{r.Start, r.End}, {day(prevStart), day(prevEnd)}}, nil,
		[]string{"activeUsers", "sessions", "screenPageViews", "engagementRate", "averageSessionDuration"}, "", 10)
	if err != nil {
		return r, err
	}
	for _, t := range totals {
		if t.dims["dateRange"] == "date_range_1" {
			r.PrevUsers, r.PrevSessions, r.PrevViews = int(t.metrics["activeUsers"]), int(t.metrics["sessions"]), int(t.metrics["screenPageViews"])
			continue
		}
		r.Users, r.Sessions, r.Views = int(t.metrics["activeUsers"]), int(t.metrics["sessions"]), int(t.metrics["screenPageViews"])
		r.Engaged, r.AvgSeconds = t.metrics["engagementRate"]*100, t.metrics["averageSessionDuration"]
	}

	days, err := c.run(ctx, p.ID, now, []string{"date"}, []string{"activeUsers", "sessions", "screenPageViews"}, "date", 100)
	if err != nil {
		return r, err
	}
	for _, d := range days {
		key := d.dims["date"]
		if len(key) == 8 {
			key = key[:4] + "-" + key[4:6] + "-" + key[6:]
		}
		r.Days = append(r.Days, d.toRow(key))
	}

	channels, err := c.run(ctx, p.ID, now, []string{"sessionDefaultChannelGroup"}, []string{"sessions", "activeUsers", "engagementRate"}, "sessions", 20)
	if err != nil {
		return r, err
	}
	for _, ch := range channels {
		r.Channels = append(r.Channels, ch.toRow(ch.dims["sessionDefaultChannelGroup"]))
	}

	sources, err := c.run(ctx, p.ID, now, []string{"sessionSource", "sessionMedium"}, []string{"sessions", "activeUsers", "engagementRate"}, "sessions", 100)
	if err != nil {
		return r, err
	}
	for _, s := range sources {
		row := s.toRow(s.dims["sessionSource"])
		row.Medium = s.dims["sessionMedium"]
		r.Sources = append(r.Sources, row)
	}

	landing, err := c.run(ctx, p.ID, now, []string{"landingPage"}, []string{"sessions", "activeUsers", "engagementRate"}, "sessions", 25)
	if err != nil {
		return r, err
	}
	for _, l := range landing {
		r.Landing = append(r.Landing, l.toRow(l.dims["landingPage"]))
	}

	pages, err := c.run(ctx, p.ID, now, []string{"pagePath"}, []string{"screenPageViews", "activeUsers"}, "screenPageViews", 25)
	if err != nil {
		return r, err
	}
	for _, pg := range pages {
		r.Pages = append(r.Pages, pg.toRow(pg.dims["pagePath"]))
	}
	return r, nil
}

// watched maps the referrer hosts Analytics reports to the sources Eavesong
// listens on, so visits can sit next to mentions.
var watched = []struct{ host, source string }{
	{"news.ycombinator.com", "hackernews"}, {"hn.algolia.com", "hackernews"},
	{"reddit.com", "reddit"},
	{"lobste.rs", "lobsters"},
	{"bsky.app", "bluesky"},
	{"t.co", "x"}, {"x.com", "x"}, {"twitter.com", "x"},
	{"github.com", "github"},
	{"dev.to", "devto"},
	{"stackoverflow.com", "stackoverflow"},
	{"youtube.com", "youtube"},
	{"facebook.com", "facebook"},
	{"producthunt.com", "producthunt"},
}

// WatchedSource names the Eavesong source behind an Analytics session
// source such as "old.reddit.com", or "" when Eavesong doesn't watch it.
func WatchedSource(sessionSource string) string {
	h := host(sessionSource)
	for _, w := range watched {
		if h == w.host || strings.HasSuffix(h, "."+w.host) {
			return w.source
		}
	}
	return ""
}
