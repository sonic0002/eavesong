package web

import (
	"context"
	"io"
	"log/slog"
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

func TestProjectSettingsSaveStaysAndPlacesDescription(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pid, err := db.SaveProject(ctx, store.Project{Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := &Server{Store: db, Monitor: &monitor.Monitor{Store: db, Log: log}, Config: config.Manager{Store: db, Box: box}, Log: log}
	handler := server.Handler()
	form := url.Values{"name": {"Acme"}, "description": {"A helpful product"}}
	req := httptest.NewRequest(http.MethodPost, "/settings/"+itoa(pid), strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/settings/"+itoa(pid)+"?saved=1" {
		t.Fatalf("save redirected to %q (status %d)", rec.Header().Get("Location"), rec.Code)
	}
	target := rec.Header().Get("Location")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	body := rec.Body.String()
	name, description, domains := strings.Index(body, `for="name"`), strings.Index(body, `for="description"`), strings.Index(body, `for="domains"`)
	if name < 0 || description <= name || domains <= description || !strings.Contains(body, "Project settings saved.") || !strings.Contains(body, "A helpful product") {
		t.Fatal("saved settings page is missing confirmation, description, or requested field order")
	}
}
