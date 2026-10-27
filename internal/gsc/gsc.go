// Package gsc reads Google Search Console data with a service account that the
// site owner added to their property as a Restricted (read-only) user.
package gsc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const scope = "https://www.googleapis.com/auth/webmasters.readonly"

// Key is the JSON key file Google Cloud gives for a service account.
type Key struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// ParseKey reads a service-account key and checks it is usable.
func ParseKey(data string) (Key, *rsa.PrivateKey, error) {
	var k Key
	if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &k); err != nil {
		return k, nil, errors.New("that is not a service-account key file (expected the JSON Google Cloud downloads)")
	}
	if k.Type != "service_account" || k.ClientEmail == "" || k.PrivateKey == "" {
		return k, nil, errors.New("the key file must be for a service account")
	}
	if k.TokenURI == "" {
		k.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return k, nil, errors.New("the key file's private key is damaged")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return k, nil, fmt.Errorf("reading the private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return k, nil, errors.New("the private key is not an RSA key")
	}
	return k, rsaKey, nil
}

// Token signs in to Google as a service account for one API scope. Search
// Console and Google Analytics share it; each asks for its own scope.
type Token struct {
	key     Key
	private *rsa.PrivateKey
	scope   string
	http    *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewToken(keyJSON, scope string) (*Token, error) {
	k, private, err := ParseKey(keyJSON)
	if err != nil {
		return nil, err
	}
	return &Token{key: k, private: private, scope: scope, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

// Email is the service account's address, which owners add as a user.
func (t *Token) Email() string { return t.key.ClientEmail }

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Get signs a JWT with the service account's key and exchanges it for an
// access token, reusing it until it is about to expire.
func (t *Token) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expires) > time.Minute {
		return t.token, nil
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iss": t.key.ClientEmail, "scope": t.scope, "aud": t.key.TokenURI, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	unsigned := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, t.private, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + b64(sig)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.key.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("could not reach Google: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error_description"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("Google did not accept the service-account key (%s)", strings.TrimSpace(out.Error+" HTTP "+fmt.Sprint(resp.StatusCode)))
	}
	t.token, t.expires = out.AccessToken, now.Add(time.Duration(out.ExpiresIn)*time.Second)
	return t.token, nil
}

// Client calls the Search Console API as one service account.
type Client struct {
	token   *Token
	http    *http.Client
	APIBase string // defaults to https://searchconsole.googleapis.com
}

func New(keyJSON string) (*Client, error) {
	t, err := NewToken(keyJSON, scope)
	if err != nil {
		return nil, err
	}
	return &Client{token: t, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (c *Client) Email() string { return c.token.Email() }

func (c *Client) call(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.token.Get(ctx)
	if err != nil {
		return err
	}
	base := c.APIBase
	if base == "" {
		base = "https://searchconsole.googleapis.com"
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
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("the service account has no access to that property; add it as a user in Search Console")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Search Console: HTTP %d: %.200s", resp.StatusCode, data)
	}
	return json.Unmarshal(data, out)
}

// Sites lists the properties the service account can read, such as
// "sc-domain:example.com" or "https://example.com/".
func (c *Client) Sites(ctx context.Context) ([]string, error) {
	var out struct {
		SiteEntry []struct {
			SiteURL         string `json:"siteUrl"`
			PermissionLevel string `json:"permissionLevel"`
		} `json:"siteEntry"`
	}
	if err := c.call(ctx, http.MethodGet, "/webmasters/v3/sites", nil, &out); err != nil {
		return nil, err
	}
	var sites []string
	for _, s := range out.SiteEntry {
		if s.PermissionLevel != "siteUnverifiedUser" {
			sites = append(sites, s.SiteURL)
		}
	}
	sort.Strings(sites)
	return sites, nil
}

// PropertyFor picks the property that covers one of a project's domains,
// preferring a Domain property over URL-prefix ones.
func PropertyFor(sites, domains []string) string {
	have := map[string]bool{}
	for _, s := range sites {
		have[s] = true
	}
	for _, d := range domains {
		d = strings.TrimPrefix(strings.ToLower(d), "www.")
		for _, candidate := range []string{"sc-domain:" + d, "https://" + d + "/", "https://www." + d + "/", "http://" + d + "/", "http://www." + d + "/"} {
			if have[candidate] {
				return candidate
			}
		}
	}
	return ""
}

// Row is one line of a Search Console breakdown.
type Row struct {
	Key         string  `json:"key"`
	Clicks      int     `json:"clicks"`
	Impressions int     `json:"impressions"`
	CTR         float64 `json:"ctr"`            // percent
	Position    float64 `json:"position"`       // average
	Page        string  `json:"page,omitempty"` // for search-and-page rows: the page shown
}

// Report is what the Search tab shows.
type Report struct {
	Engine      string    `json:"engine,omitempty"` // "" for Google, "bing" for Bing Webmaster Tools
	Property    string    `json:"property"`
	Start       string    `json:"start"`
	End         string    `json:"end"`
	FetchedAt   time.Time `json:"fetched_at"`
	Clicks      int       `json:"clicks"`
	Impressions int       `json:"impressions"`
	Days        []Row     `json:"days"`
	Queries     []Row     `json:"queries"`
	Pages       []Row     `json:"pages"`
	PageTwo     []Row     `json:"page_two"` // average position 8 to 20, with real impressions
	Sitemaps    []Sitemap `json:"sitemaps"`
	// Which page ranks for each search, and searches in the two halves of
	// the period, for the recommendations.
	QueryPages []Row `json:"query_pages"`
	Earlier    []Row `json:"earlier"`
	Recent     []Row `json:"recent"`
}

type Sitemap struct {
	Path           string `json:"path"`
	LastDownloaded string `json:"last_downloaded"`
	Errors         int    `json:"errors"`
	Warnings       int    `json:"warnings"`
	Status         string `json:"status,omitempty"` // Bing's word for the sitemap's state
}

func (c *Client) rows(ctx context.Context, property, start, end, dimension string, limit int) ([]Row, error) {
	return c.rowsBy(ctx, property, start, end, []string{dimension}, limit)
}

func (c *Client) rowsBy(ctx context.Context, property, start, end string, dimensions []string, limit int) ([]Row, error) {
	var out struct {
		Rows []struct {
			Keys        []string `json:"keys"`
			Clicks      float64  `json:"clicks"`
			Impressions float64  `json:"impressions"`
			CTR         float64  `json:"ctr"`
			Position    float64  `json:"position"`
		} `json:"rows"`
	}
	err := c.call(ctx, http.MethodPost, "/webmasters/v3/sites/"+url.QueryEscape(property)+"/searchAnalytics/query",
		map[string]any{"startDate": start, "endDate": end, "dimensions": dimensions, "rowLimit": limit}, &out)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(out.Rows))
	for _, r := range out.Rows {
		row := Row{Key: r.Keys[0], Clicks: int(r.Clicks), Impressions: int(r.Impressions), CTR: r.CTR * 100, Position: r.Position}
		if len(r.Keys) > 1 {
			row.Page = r.Keys[1]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Fetch builds the last 28 days of search data for a property. Google reports
// with a two to three day delay, so the window ends two days ago.
func (c *Client) Fetch(ctx context.Context, property string) (Report, error) {
	end := time.Now().UTC().AddDate(0, 0, -2)
	start := end.AddDate(0, 0, -27)
	r := Report{Property: property, Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"), FetchedAt: time.Now().UTC()}
	var err error
	if r.Days, err = c.rows(ctx, property, r.Start, r.End, "date", 100); err != nil {
		return r, err
	}
	if r.Queries, err = c.rows(ctx, property, r.Start, r.End, "query", 250); err != nil {
		return r, err
	}
	if r.Pages, err = c.rows(ctx, property, r.Start, r.End, "page", 250); err != nil {
		return r, err
	}
	if r.QueryPages, err = c.rowsBy(ctx, property, r.Start, r.End, []string{"query", "page"}, 1000); err != nil {
		return r, err
	}
	middle := start.AddDate(0, 0, 14)
	if r.Earlier, err = c.rows(ctx, property, r.Start, middle.AddDate(0, 0, -1).Format("2006-01-02"), "query", 250); err != nil {
		return r, err
	}
	if r.Recent, err = c.rows(ctx, property, middle.Format("2006-01-02"), r.End, "query", 250); err != nil {
		return r, err
	}
	for _, d := range r.Days {
		r.Clicks += d.Clicks
		r.Impressions += d.Impressions
	}
	byImpressions := func(rows []Row) {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Impressions > rows[j].Impressions })
	}
	byImpressions(r.Queries)
	byImpressions(r.Pages)
	for _, q := range r.Queries {
		if q.Position >= 8 && q.Position <= 20 && q.Impressions >= 5 {
			r.PageTwo = append(r.PageTwo, q)
		}
	}
	var maps struct {
		Sitemap []struct {
			Path           string `json:"path"`
			LastDownloaded string `json:"lastDownloaded"`
			Errors         string `json:"errors"`
			Warnings       string `json:"warnings"`
		} `json:"sitemap"`
	}
	if c.call(ctx, http.MethodGet, "/webmasters/v3/sites/"+url.QueryEscape(property)+"/sitemaps", nil, &maps) == nil {
		for _, m := range maps.Sitemap {
			var e, w int
			fmt.Sscan(m.Errors, &e)
			fmt.Sscan(m.Warnings, &w)
			r.Sitemaps = append(r.Sitemaps, Sitemap{Path: m.Path, LastDownloaded: m.LastDownloaded, Errors: e, Warnings: w})
		}
	}
	return r, nil
}
