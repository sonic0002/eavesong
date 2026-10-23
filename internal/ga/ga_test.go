package ga

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
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

func TestPropertiesAndFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case r.Header.Get("Authorization") != "Bearer tok":
			http.Error(w, "no token", http.StatusUnauthorized)
		case r.URL.Path == "/v1beta/accountSummaries":
			w.Write([]byte(`{"accountSummaries":[{"propertySummaries":[{"property":"properties/1","displayName":"Blog"},{"property":"properties/2","displayName":"Acme site"}]}]}`))
		case r.URL.Path == "/v1beta/properties/1/dataStreams":
			w.Write([]byte(`{"dataStreams":[{"webStreamData":{"defaultUri":"https://blog.example.org"}}]}`))
		case r.URL.Path == "/v1beta/properties/2/dataStreams":
			w.Write([]byte(`{"dataStreams":[{"webStreamData":{"defaultUri":"https://www.acme.dev"}}]}`))
		case r.URL.Path == "/v1beta/properties/2:runReport":
			var body struct {
				DateRanges []any `json:"dateRanges"`
				Dimensions []struct{ Name string }
			}
			json.NewDecoder(r.Body).Decode(&body)
			if len(body.Dimensions) == 0 {
				if len(body.DateRanges) != 2 {
					t.Errorf("totals should compare two periods, got %d", len(body.DateRanges))
				}
				w.Write([]byte(`{"dimensionHeaders":[{"name":"dateRange"}],"metricHeaders":[{"name":"activeUsers"},{"name":"sessions"},{"name":"screenPageViews"},{"name":"engagementRate"},{"name":"averageSessionDuration"}],
					"rows":[{"dimensionValues":[{"value":"date_range_0"}],"metricValues":[{"value":"120"},{"value":"150"},{"value":"400"},{"value":"0.62"},{"value":"75.5"}]},
					        {"dimensionValues":[{"value":"date_range_1"}],"metricValues":[{"value":"80"},{"value":"100"},{"value":"300"},{"value":"0.5"},{"value":"60"}]}]}`))
				return
			}
			switch body.Dimensions[0].Name {
			case "date":
				w.Write([]byte(`{"dimensionHeaders":[{"name":"date"}],"metricHeaders":[{"name":"activeUsers"},{"name":"sessions"},{"name":"screenPageViews"}],"rows":[{"dimensionValues":[{"value":"20261001"}],"metricValues":[{"value":"5"},{"value":"6"},{"value":"9"}]}]}`))
			case "sessionSource":
				w.Write([]byte(`{"dimensionHeaders":[{"name":"sessionSource"},{"name":"sessionMedium"}],"metricHeaders":[{"name":"sessions"},{"name":"activeUsers"},{"name":"engagementRate"}],"rows":[{"dimensionValues":[{"value":"news.ycombinator.com"},{"value":"referral"}],"metricValues":[{"value":"40"},{"value":"35"},{"value":"0.4"}]}]}`))
			default:
				w.Write([]byte(`{"dimensionHeaders":[{"name":"` + body.Dimensions[0].Name + `"}],"metricHeaders":[{"name":"sessions"}],"rows":[{"dimensionValues":[{"value":"x"}],"metricValues":[{"value":"3"}]}]}`))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(testKey(t, srv.URL+"/token"))
	if err != nil {
		t.Fatal(err)
	}
	c.AdminBase, c.DataBase = srv.URL, srv.URL
	ctx := context.Background()
	props, err := c.Properties(ctx)
	if err != nil || len(props) != 2 {
		t.Fatalf("Properties = %+v, %v", props, err)
	}
	p, ok := PropertyFor(props, []string{"acme.dev"})
	if !ok || p.ID != "properties/2" {
		t.Fatalf("PropertyFor matched %+v, %v; want the property whose stream is www.acme.dev", p, ok)
	}
	r, err := c.Fetch(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Users != 120 || r.Sessions != 150 || r.PrevSessions != 100 || r.Engaged != 62 {
		t.Errorf("totals wrong: %+v", r)
	}
	if len(r.Days) != 1 || r.Days[0].Key != "2026-10-01" {
		t.Errorf("days = %+v", r.Days)
	}
	if len(r.Sources) != 1 || r.Sources[0].Sessions != 40 || r.Sources[0].Medium != "referral" {
		t.Errorf("sources = %+v", r.Sources)
	}
}

func TestPermissionErrorSaysWhatToDo(t *testing.T) {
	err := explain(http.StatusForbidden, []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED"}}`), "reader@x.iam.gserviceaccount.com")
	if !strings.Contains(err.Error(), "with the **Viewer** role") {
		t.Errorf("got %v", err)
	}
	err = explain(http.StatusForbidden, []byte(`{"error":{"code":403,"message":"Google Analytics Data API has not been used in project 1 before","status":"PERMISSION_DENIED","details":[{"reason":"SERVICE_DISABLED"}]}}`), "r@x")
	if !strings.Contains(err.Error(), "Enable the **Google Analytics Admin API**") {
		t.Errorf("got %v", err)
	}
}

func TestWatchedSource(t *testing.T) {
	for in, want := range map[string]string{
		"news.ycombinator.com": "hackernews", "old.reddit.com": "reddit", "reddit.com": "reddit",
		"t.co": "x", "l.facebook.com": "facebook", "m.youtube.com": "youtube", "bsky.app": "bluesky",
		"google": "", "notreddit.com": "", "(direct)": "",
	} {
		if got := WatchedSource(in); got != want {
			t.Errorf("WatchedSource(%q) = %q, want %q", in, got, want)
		}
	}
}
