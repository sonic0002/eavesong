package gsc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func testKey(t *testing.T, tokenURI string) string {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	key, _ := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "reader@test.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": tokenURI,
	})
	return string(key)
}

func TestFetchReport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			r.ParseForm()
			if parts := strings.Split(r.Form.Get("assertion"), "."); len(parts) != 3 {
				t.Errorf("assertion is not a signed JWT: %q", r.Form.Get("assertion"))
			}
			w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer tok":
			http.Error(w, "no token", http.StatusUnauthorized)
		case r.URL.Path == "/webmasters/v3/sites":
			w.Write([]byte(`{"siteEntry":[{"siteUrl":"sc-domain:acme.dev","permissionLevel":"siteRestrictedUser"},{"siteUrl":"https://other.org/","permissionLevel":"siteUnverifiedUser"}]}`))
		case strings.HasSuffix(r.URL.EscapedPath(), "/searchAnalytics/query"):
			if !strings.Contains(r.URL.EscapedPath(), url.QueryEscape("sc-domain:acme.dev")) {
				t.Errorf("property not encoded in %s", r.URL.EscapedPath())
			}
			var body struct{ Dimensions []string }
			json.NewDecoder(r.Body).Decode(&body)
			switch body.Dimensions[0] {
			case "date":
				w.Write([]byte(`{"rows":[{"keys":["2026-10-01"],"clicks":3,"impressions":100,"ctr":0.03,"position":7.5},{"keys":["2026-10-02"],"clicks":5,"impressions":140,"ctr":0.035,"position":7.1}]}`))
			case "query":
				w.Write([]byte(`{"rows":[{"keys":["acme cli"],"clicks":4,"impressions":90,"ctr":0.044,"position":3.2},{"keys":["acme vs rival"],"clicks":0,"impressions":40,"ctr":0,"position":11.4}]}`))
			default:
				w.Write([]byte(`{"rows":[{"keys":["https://acme.dev/docs"],"clicks":8,"impressions":240,"ctr":0.033,"position":6}]}`))
			}
		case strings.HasSuffix(r.URL.Path, "/sitemaps"):
			w.Write([]byte(`{"sitemap":[{"path":"https://acme.dev/sitemap.xml","errors":"0","warnings":"1"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(testKey(t, srv.URL+"/token"))
	if err != nil {
		t.Fatal(err)
	}
	c.APIBase = srv.URL
	ctx := context.Background()
	sites, err := c.Sites(ctx)
	if err != nil || len(sites) != 1 {
		t.Fatalf("Sites = %v, %v (unverified properties must be left out)", sites, err)
	}
	property := PropertyFor(sites, []string{"www.acme.dev"})
	if property != "sc-domain:acme.dev" {
		t.Fatalf("PropertyFor = %q", property)
	}
	r, err := c.Fetch(ctx, property)
	if err != nil {
		t.Fatal(err)
	}
	if r.Clicks != 8 || r.Impressions != 240 || len(r.Queries) != 2 || len(r.Pages) != 1 {
		t.Errorf("report totals wrong: %+v", r)
	}
	if len(r.PageTwo) != 1 || r.PageTwo[0].Key != "acme vs rival" {
		t.Errorf("page two = %+v", r.PageTwo)
	}
	if len(r.Sitemaps) != 1 || r.Sitemaps[0].Warnings != 1 {
		t.Errorf("sitemaps = %+v", r.Sitemaps)
	}
}

func TestParseKeyRejectsOtherFiles(t *testing.T) {
	for _, bad := range []string{"", "not json", `{"type":"authorized_user"}`} {
		if _, _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) should fail", bad)
		}
	}
}
