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

	"github.com/sonic0002/eavesong/internal/bing"
	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/insight"
	"github.com/sonic0002/eavesong/internal/monitor"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
)

func TestSearchTabSwitchesBetweenGoogleAndBing(t *testing.T) {
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
	report := gsc.Report{Property: "sc-domain:acme.dev", Start: "2026-09-01", End: "2026-09-28", Clicks: 41, Impressions: 900,
		Queries: []gsc.Row{{Key: "acme cli", Impressions: 300, Clicks: 5, CTR: 1.7, Position: 12}}}
	data, _ := json.Marshal(report)
	if err := db.SaveSearchReport(ctx, pid, string(data), ""); err != nil {
		t.Fatal(err)
	}
	report.Engine, report.Property, report.Clicks = "bing", "https://acme.dev/", 7
	data, _ = json.Marshal(report)
	if err := db.SaveBingReport(ctx, pid, string(data), ""); err != nil {
		t.Fatal(err)
	}

	priv, _ := rsa.GenerateKey(rand.Reader, 2048)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	key, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "r@x.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": "http://127.0.0.1:1/token"})
	google, err := gsc.New(string(key))
	if err != nil {
		t.Fatal(err)
	}
	offline := bing.New("k")
	offline.APIBase = "http://127.0.0.1:1"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	box, _ := secret.New(make([]byte, 32))
	server := func(g []*gsc.Client, b []*bing.Client) *Server {
		m := &monitor.Monitor{Store: db, Log: log}
		m.ConfigureSearch(g)
		m.ConfigureBing(b)
		return &Server{Store: db, Monitor: m, Config: config.Manager{Store: db, Box: box}, Log: log}
	}
	get := func(s *Server, path string) string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("GET %s: status %d: %.500s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	tab := "/p/" + itoa(pid) + "/search"

	// Google only: the tab looks as it did before Bing support.
	body := get(server([]*gsc.Client{google}, nil), tab)
	if strings.Contains(body, `aria-label="Search engine"`) || !strings.Contains(body, "Clicks from Google") || !strings.Contains(body, "Google Search Console, sc-domain:acme.dev") {
		t.Errorf("Google-only Search tab changed: %.300s", body)
	}

	// Both: a switch, Google by default, Bing on request.
	both := server([]*gsc.Client{google}, []*bing.Client{offline})
	body = get(both, tab)
	if !strings.Contains(body, `aria-label="Search engine"`) || !strings.Contains(body, "Clicks from Google") {
		t.Error("with both connected the tab shows Google and a switch")
	}
	body = get(both, tab+"?engine=bing")
	for _, want := range []string{"Bing Webmaster Tools, https://acme.dev/", "Clicks from Bing", `<input type="hidden" name="engine" value="bing">`, "Copilot", "Bing shows"} {
		if !strings.Contains(body, want) {
			t.Errorf("Bing view is missing %q", want)
		}
	}
	if strings.Contains(body, "Clicks from Google") {
		t.Error("Bing view shows Google's figures")
	}

	// Bing only: the tab opens on Bing without a switch.
	body = get(server(nil, []*bing.Client{offline}), tab)
	if strings.Contains(body, `aria-label="Search engine"`) || !strings.Contains(body, "Clicks from Bing") {
		t.Error("a Bing-only install opens the Search tab on Bing")
	}

	// Marking a Bing recommendation done returns to the Bing view.
	recs := insight.Analyze(report)
	form := url.Values{"key": {insight.Key(recs[0])}, "status": {"done"}}
	req := httptest.NewRequest("POST", tab+"/recommendation", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	both.Handler().ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); rec.Code != 303 || loc != tab+"?engine=bing" {
		t.Errorf("recommendation: status %d, location %q: %s", rec.Code, loc, rec.Body.String())
	}
}
