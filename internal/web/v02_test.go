package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/monitor"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
)

func v02Server(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Server{Store: db, Monitor: &monitor.Monitor{Store: db, Log: log}, Config: config.Manager{Store: db, Box: box}, Log: log}, db
}

func TestFirstRunSetupProgressesThroughOptionalSteps(t *testing.T) {
	s, db := v02Server(t)
	h := s.Handler()
	post := func(path string, values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := post("/settings/0", url.Values{"name": {"Acme"}, "domains": {"acme.dev"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings?setup=accounts#mine" {
		t.Fatalf("first step: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = post("/setup/next", nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings#drafts" {
		t.Fatalf("second step: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = post("/setup/finish", nil)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/p/") {
		t.Fatalf("finish: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	values, err := db.Settings(context.Background())
	if err != nil || values["setup_stage"] != "" {
		t.Fatalf("setup stage retained: %q, %v", values["setup_stage"], err)
	}
}

func TestPortableExportOmitsSecretsAndImportsProjects(t *testing.T) {
	s, db := v02Server(t)
	ctx := context.Background()
	if err := s.Config.Save(ctx, config.Settings{AIProvider: "anthropic", AIKey: "secret-ai-key", AIBaseURL: "https://secret.example/token", SlackWebhook: "https://secret.example/hook", GenericWebhook: "https://secret.example/events"}); err != nil {
		t.Fatal(err)
	}
	_, err := db.SaveProject(ctx, store.Project{Name: "Acme", DraftSettings: store.DraftSettings{Description: "Useful product facts"}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings/export", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	for _, secret := range []string{"secret-ai-key", "secret.example"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("export leaked %q", secret)
		}
	}
	if strings.Contains(rec.Body.String(), "smtp_") {
		t.Fatal("export still contains removed SMTP settings")
	}
	var exported portableSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil || len(exported.Projects) != 1 || exported.Projects[0].Description != "Useful product facts" {
		t.Fatalf("export contents: %+v, %v", exported, err)
	}
	exported.Projects = append(exported.Projects, portableProject{Name: "Second", Domains: []string{"second.dev"}})
	data, _ := json.Marshal(exported)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	f, err := mw.CreateFormFile("settings_file", "settings.json")
	if err != nil {
		t.Fatal(err)
	}
	f.Write(data)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/settings/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	projects, err := db.Projects(ctx)
	if err != nil || len(projects) != 2 {
		t.Fatalf("imported projects: %d, %v", len(projects), err)
	}
	cfg, err := s.Config.Load(ctx)
	if err != nil || cfg.AIKey != "secret-ai-key" || cfg.SlackWebhook != "https://secret.example/hook" {
		t.Fatalf("import changed current secrets: %v", err)
	}
}

func TestNotificationSettingsSavesWebhookAndDigest(t *testing.T) {
	s, _ := v02Server(t)
	values := url.Values{"slack_webhook": {"https://hooks.slack.com/services/example"}, "notify_weekly": {"1"}, "smtp_host": {"mail.example.com"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/notifications", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save notification settings: %d %s", rec.Code, rec.Body.String())
	}
	cfg, err := s.Config.Load(context.Background())
	if err != nil || cfg.SlackWebhook != "https://hooks.slack.com/services/example" || cfg.NotifyWeekly != "1" {
		t.Fatalf("saved notification settings were incomplete: %v", err)
	}
	stored, err := s.Config.Store.Settings(context.Background())
	if err != nil || stored["smtp_host"] != "" {
		t.Fatalf("obsolete SMTP field was stored: %v", err)
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if strings.Contains(rec.Body.String(), "Email (SMTP)") || strings.Contains(rec.Body.String(), "smtp_host") {
		t.Fatal("SMTP setup is still shown in settings")
	}
}
