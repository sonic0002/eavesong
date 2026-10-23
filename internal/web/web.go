// Package web serves the Eavesong interface: the mention inbox, project
// settings and reports. It is protected by a single password (HTTP basic auth)
// and only writes to the local database; it never posts anywhere.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/ai"
	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/insight"
	"github.com/sonic0002/eavesong/internal/monitor"
	"github.com/sonic0002/eavesong/internal/store"
)

//go:embed templates/*.html static/*
var files embed.FS

type Server struct {
	Store    *store.Store
	Monitor  *monitor.Monitor
	Config   config.Manager
	Password string // empty disables sign-in (only for local use)
	Log      *slog.Logger

	tmpl *template.Template
}

func (s *Server) Handler() http.Handler {
	s.tmpl = template.Must(template.New("").Funcs(template.FuncMap{
		"ago":           ago,
		"recKey":        insight.Key,
		"join":          func(v []string) string { return strings.Join(v, ", ") },
		"lines":         func(v []string) string { return strings.Join(v, "\n") },
		"date":          func(t time.Time) string { return t.Format("Jan 2, 15:04 UTC") },
		"sourceName":    sourceName,
		"sourceMark":    sourceMark,
		"contextAuthor": contextAuthor,
		// group passes a page's data and a list to a shared template block.
		"group":   func(root any, list any) map[string]any { return map[string]any{"Root": root, "List": list} },
		"clean":   clean,
		"pulse":   pulse,
		"failure": failure,
		"bar":     bar,
		"plural": func(n int, one, many string) string {
			if n == 1 {
				return "1 " + one
			}
			return strconv.Itoa(n) + " " + many
		},
	}).ParseFS(files, "templates/*.html"))

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.FileServerFS(files))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /{$}", s.home)
	mux.HandleFunc("GET /p/{id}", s.inbox)
	mux.HandleFunc("GET /p/{id}/report", s.report)
	mux.HandleFunc("GET /p/{id}/activity", s.activity)
	mux.HandleFunc("GET /p/{id}/search", s.search)
	mux.HandleFunc("POST /p/{id}/search/refresh", s.refreshSearch)
	mux.HandleFunc("GET /p/{id}/search/status", s.searchStatus)
	mux.HandleFunc("GET /p/{id}/traffic", s.traffic)
	mux.HandleFunc("POST /p/{id}/traffic/refresh", s.refreshTraffic)
	mux.HandleFunc("GET /p/{id}/traffic/status", s.trafficStatus)
	mux.HandleFunc("POST /p/{id}/search/plan", s.searchPlan)
	mux.HandleFunc("POST /p/{id}/check", s.checkNow)
	mux.HandleFunc("POST /m/{id}/status", s.setStatus)
	mux.HandleFunc("POST /m/{id}/draft", s.draft)
	mux.HandleFunc("POST /m/{id}/usage", s.draftUsage)
	mux.HandleFunc("POST /p/{id}/search/recommendation", s.recommendation)
	mux.HandleFunc("GET /settings", s.globalSettings)
	mux.HandleFunc("POST /settings", s.saveGlobalSettings)
	mux.HandleFunc("POST /settings/test-ai", s.testAI)
	mux.HandleFunc("GET /settings/export", s.exportSettings)
	mux.HandleFunc("POST /settings/import", s.importSettings)
	mux.HandleFunc("POST /setup/next", s.setupNext)
	mux.HandleFunc("POST /setup/finish", s.setupFinish)
	mux.HandleFunc("POST /settings/notifications", s.saveNotificationSettings)
	mux.HandleFunc("POST /settings/accounts", s.addAccount)
	mux.HandleFunc("POST /settings/accounts/{id}/remove", s.removeAccount)
	mux.HandleFunc("POST /settings/accounts/{id}/projects", s.setAccountProjects)
	mux.HandleFunc("GET /settings/{id}", s.settings)
	mux.HandleFunc("POST /settings/{id}", s.saveSettings)
	return s.secure(mux)
}

// secure adds sign-in, refuses cross-site form posts and sets safe headers.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'")
		if r.URL.Path != "/healthz" && s.Password != "" {
			_, pass, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.Password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="Eavesong", charset="UTF-8"`)
				http.Error(w, "Sign in with the password set in EAVESONG_PASSWORD.", http.StatusUnauthorized)
				return
			}
		}
		if r.Method == http.MethodPost {
			origin := r.Header.Get("Origin")
			if origin == "" {
				origin = r.Header.Get("Referer")
			}
			if u, err := url.Parse(origin); origin != "" && (err != nil || u.Host != r.Host) {
				http.Error(w, "Cross-site form posts are refused.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) render(w http.ResponseWriter, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.Log.Error("rendering", "template", name, "err", err)
	}
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong; see the server log.", http.StatusInternalServerError)
}

func id(r *http.Request) int64 {
	n, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n
}

func (s *Server) base(ctx context.Context, current int64) (map[string]any, error) {
	projects, err := s.Store.Projects(ctx)
	return map[string]any{"Projects": projects, "Current": current}, err
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.Projects(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(projects) == 0 {
		http.Redirect(w, r, "/settings/0", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/p/"+strconv.FormatInt(projects[0].ID, 10), http.StatusSeeOther)
}

func (s *Server) inbox(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if !q.Has("status") {
		status = "new"
	}
	f := store.Filter{Status: status, Source: q.Get("source"), About: q.Get("about"), IncludeOurs: q.Get("ours") == "1"}
	mentions, err := s.Store.Mentions(ctx, p.ID, f)
	if err != nil {
		s.fail(w, err)
		return
	}
	checks, err := s.Store.LastChecks(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	data, err := s.base(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	data["Project"], data["Mentions"], data["Checks"], data["Filter"] = p, mentions, checks, f
	data["AI"] = s.Monitor.HasDrafter()
	data["Title"], data["Tab"] = p.Name, "inbox"
	data["L"] = links{ProjectID: p.ID, F: f}
	if data["StatusCounts"], err = s.Store.StatusCounts(ctx, p.ID, f.IncludeOurs); err != nil {
		s.fail(w, err)
		return
	}
	if data["SourceCounts"], err = s.Store.SourceCounts(ctx, p.ID, f.Status, f.IncludeOurs); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.addPulse(ctx, p.ID, data); err != nil {
		s.fail(w, err)
		return
	}
	data["Checking"] = r.URL.Query().Has("checking")
	if data["Activity"], err = s.Store.Activity(ctx, p.ID, ""); err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "inbox.html", data)
}

func (s *Server) checkNow(w http.ResponseWriter, r *http.Request) {
	s.Monitor.CheckNow()
	http.Redirect(w, r, "/p/"+r.PathValue("id")+"?checking=1", http.StatusSeeOther)
}

func back(w http.ResponseWriter, r *http.Request, fallback string) {
	if ref := r.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host == r.Host {
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}

func (s *Server) setStatus(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.SetStatus(r.Context(), id(r), r.FormValue("status")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	back(w, r, "/")
}

func (s *Server) draft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.Monitor.HasDrafter() {
		http.Error(w, "Reply drafts are off. Choose an AI provider in Settings.", http.StatusBadRequest)
		return
	}
	m, err := s.Store.Mention(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.Store.Project(ctx, m.ProjectID)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Monitor.DraftOne(ctx, p, m); err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, "/p/"+strconv.FormatInt(p.ID, 10))
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := s.base(ctx, id(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	p := store.Project{}
	if id(r) != 0 {
		if p, err = s.Store.Project(ctx, id(r)); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	data["Project"], data["Platforms"] = p, store.Platforms
	data["Saved"] = r.URL.Query().Has("saved")
	data["Title"], data["Tab"] = firstNonEmpty(p.Name, "New project"), "settings"
	accounts, err := s.Config.Accounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	projects, _ := data["Projects"].([]store.Project)
	var mine []shownAccount
	for _, a := range accounts {
		if config.PlatformKind(a.Platform) == config.KindAccount {
			shown := showAccount(a, projects)
			shown.Here = p.ID == 0 || shown.All || shown.In[p.ID]
			mine = append(mine, shown)
		}
	}
	data["MyAccounts"] = mine
	if p.ID != 0 {
		if err := s.addPulse(ctx, p.ID, data); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.render(w, "settings.html", data)
}

func list(form string) []string {
	var out []string
	for _, line := range strings.FieldsFunc(form, func(r rune) bool { return r == '\n' || r == ',' }) {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	firstProject := false
	if id(r) == 0 {
		projects, err := s.Store.Projects(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		firstProject = len(projects) == 0
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Project settings are too large.", 400)
		return
	}
	p := store.Project{
		DraftSettings: store.DraftSettings{Description: r.FormValue("description"), Tone: r.FormValue("tone"), ReplyLength: r.FormValue("reply_length"), AvoidWords: r.FormValue("avoid_words"), Examples: r.FormValue("examples"), ReadLinks: r.FormValue("read_links") == "1"},
		ID:            id(r),
		Name:          strings.TrimSpace(r.FormValue("name")),
		Domains:       list(strings.ToLower(r.FormValue("domains"))),
		Keywords:      list(r.FormValue("keywords")),
		Competitors:   list(r.FormValue("competitors")),
	}
	for _, pl := range store.Platforms {
		for _, handle := range list(r.FormValue("own_" + pl.Key)) {
			if handle = store.CleanHandle(handle); handle != "" {
				p.OwnAccounts = append(p.OwnAccounts, pl.Key+":"+handle)
			}
		}
	}
	for i, d := range p.Domains { // accept pasted URLs
		d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
		p.Domains[i] = strings.TrimPrefix(strings.TrimSuffix(d, "/"), "www.")
	}
	saved, err := s.Store.SaveProject(r.Context(), p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.ID = saved
	if err := s.Store.RefreshOurs(r.Context(), p); err != nil {
		s.fail(w, err)
		return
	}
	if r.FormValue("accounts_shown") == "1" {
		if err := s.saveAccountsForProject(r, p.ID); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.Monitor.CheckNow()
	if id(r) != 0 {
		http.Redirect(w, r, "/settings/"+strconv.FormatInt(saved, 10)+"?saved=1", http.StatusSeeOther)
		return
	}
	if !firstProject {
		http.Redirect(w, r, "/p/"+strconv.FormatInt(saved, 10)+"?checking=1", http.StatusSeeOther)
		return
	}
	if err := s.Store.SaveSettings(r.Context(), map[string]string{"setup_stage": "accounts"}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?setup=accounts#mine", http.StatusSeeOther)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days != 30 && days != 90 {
		days = 7
	}
	since := time.Now().AddDate(0, 0, -days)
	bySource, err := s.Store.Breakdown(ctx, p.ID, since, "source")
	if err != nil {
		s.fail(w, err)
		return
	}
	all, err := s.Store.Mentions(ctx, p.ID, store.Filter{Limit: 5000})
	if err != nil {
		s.fail(w, err)
		return
	}
	// Share of voice: how often each tracked name came up in the period.
	counts := map[string]int{}
	var recent []store.Mention
	for _, m := range all {
		if m.PostedAt.Before(since) {
			continue
		}
		recent = append(recent, m)
		for _, t := range m.Terms {
			counts[t]++
		}
	}
	var voice []store.Count
	for _, t := range append(append(append([]string{}, p.Domains...), p.Keywords...), p.Competitors...) {
		voice = append(voice, store.Count{Label: t, N: counts[t]})
	}
	sort.SliceStable(voice, func(i, j int) bool { return voice[i].N > voice[j].N })
	attention := []store.Mention{}
	for _, m := range recent {
		if m.Decision == "attention" || m.Decision == "suggest" {
			attention = append(attention, m)
		}
	}
	data, err := s.base(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	data["Project"], data["Days"], data["Total"], data["BySource"], data["Voice"], data["Attention"] = p, days, len(recent), bySource, voice, attention
	data["Title"], data["Tab"] = p.Name+" report", "report"
	maxVoice := 0
	for _, v := range voice {
		if v.N > maxVoice {
			maxVoice = v.N
		}
	}
	isProduct := map[string]bool{}
	for _, t := range append(append([]string{}, p.Domains...), p.Keywords...) {
		isProduct[t] = true
	}
	data["MaxVoice"], data["IsProduct"] = maxVoice, isProduct
	if err := s.addPulse(ctx, p.ID, data); err != nil {
		s.fail(w, err)
		return
	}
	s.render(w, "report.html", data)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + " min ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + " h ago"
	}
	return strconv.Itoa(int(d.Hours()/24)) + " days ago"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// addPulse adds the 14-day mentions line shown next to the project name.
func (s *Server) addPulse(ctx context.Context, projectID int64, data map[string]any) error {
	days, err := s.Store.Daily(ctx, projectID, 14)
	if err != nil {
		return err
	}
	total := 0
	for _, d := range days {
		total += d.N
	}
	data["Pulse"], data["PulseTotal"] = pulse(days, 168, 40), total
	return nil
}

// globalSettings shows the installation settings: AI provider and source credentials.
func (s *Server) globalSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	data, err := s.base(ctx, -1)
	if err != nil {
		s.fail(w, err)
		return
	}
	cfg, err := s.Config.Load(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	_, aiName := cfg.Drafter()
	names := s.Monitor.SourceNames()
	q := r.URL.Query()
	data["Title"], data["Settings"], data["AIName"], data["SourceNames"] = "Settings", cfg, aiName, names
	data["KeyHint"] = config.Hint(cfg.AIKey)
	accounts, err := s.Config.Accounts(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	projects, _ := data["Projects"].([]store.Project)
	byPlatform := map[string][]shownAccount{}
	for _, a := range accounts {
		byPlatform[a.Platform] = append(byPlatform[a.Platform], showAccount(a, projects))
	}
	for _, pl := range config.AccountPlatforms {
		key := "KeyPlatforms"
		if pl.Kind == config.KindAccount {
			key = "MinePlatforms"
		}
		list, _ := data[key].([]config.Platform)
		data[key] = append(list, pl)
	}
	data["Scoped"] = q.Has("scoped")
	data["XCap"], data["XUsed"] = cfg.XCap(), config.XBudget{Store: s.Store, Cap: cfg.XCap()}.Used(ctx)
	data["Accounts"], data["AccountError"], data["AccountAdded"], data["Open"] = byPlatform, q.Get("account_error"), q.Get("added"), q.Get("open")
	data["AccountNote"] = q.Get("note")
	data["DefaultModel"], data["DefaultDeepSeekModel"], data["DefaultGeminiModel"] = config.DefaultModel, config.DefaultDeepSeekModel, config.DefaultGeminiModel
	data["Saved"], data["Tested"], data["TestError"], data["FormError"] = q.Has("saved"), q.Has("tested"), q.Get("test_error"), q.Get("error")
	data["Imported"] = q.Has("imported")
	data["Notifications"] = cfg
	data["NotificationSaved"] = q.Has("notifications_saved")
	data["SlackHint"], data["DiscordHint"], data["WebhookHint"] = config.Hint(cfg.SlackWebhook), config.Hint(cfg.DiscordWebhook), config.Hint(cfg.GenericWebhook)
	if savedSettings, err := s.Store.Settings(ctx); err == nil {
		data["SetupStage"] = savedSettings["setup_stage"]
	}
	s.render(w, "global.html", data)
}

// keep returns the new value of a secret field: the typed value, nothing when
// the "remove" box is ticked, otherwise the saved value unchanged.
func keep(r *http.Request, field, saved string) string {
	if r.FormValue("remove_"+field) == "1" {
		return ""
	}
	if v := strings.TrimSpace(r.FormValue(field)); v != "" {
		return v
	}
	return saved
}

func (s *Server) saveGlobalSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	old, err := s.Config.Load(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	cfg := old
	cfg.AIProvider = r.FormValue("ai_provider")
	cfg.AIModel = strings.TrimSpace(r.FormValue("ai_model"))
	cfg.AIBaseURL = strings.TrimSpace(r.FormValue("ai_base_url"))
	cfg.AIKey = keep(r, "ai_key", old.AIKey)
	cfg.XDailyCap = strings.TrimSpace(r.FormValue("x_daily_cap"))
	if n, err := strconv.Atoi(cfg.XDailyCap); cfg.XDailyCap != "" && (err != nil || n < 0) {
		http.Redirect(w, r, "/settings?error="+url.QueryEscape("the daily limit for X must be a whole number, 0 or more"), http.StatusSeeOther)
		return
	}
	if cfg.AIProvider != old.AIProvider && r.FormValue("ai_key") == "" && r.FormValue("remove_ai_key") != "1" {
		cfg.AIKey = "" // a key for one provider is no use for another
	}
	if cfg.AIProvider != old.AIProvider && cfg.AIModel == old.AIModel {
		cfg.AIModel = "" // use the new provider's default rather than the old model
	}
	if cfg.AIProvider == "openai" && cfg.AIBaseURL == "" {
		cfg.AIBaseURL = "https://api.openai.com/v1"
	}
	if err := cfg.Validate(); err != nil {
		http.Redirect(w, r, "/settings?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	if err := s.Config.Save(ctx, cfg); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Reconfigure(ctx); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
}

// testAI sends one sample mention to the saved provider and reports the result.
func (s *Server) testAI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cfg, err := s.Config.Load(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	drafter, _ := cfg.Drafter()
	if drafter == nil {
		http.Redirect(w, r, "/settings?test_error="+url.QueryEscape("Reply drafts are off; choose a provider and save first."), http.StatusSeeOther)
		return
	}
	_, err = drafter.Draft(ctx, ai.Request{
		Product: "Eavesong", Domains: []string{"example.com"}, Source: "hackernews", Where: "comment",
		Author: "sample", Text: "Does Eavesong work with a local model?", About: "product", Terms: []string{"Eavesong"},
	})
	if err != nil {
		http.Redirect(w, r, "/settings?test_error="+url.QueryEscape(explain(err, cfg)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?tested=1", http.StatusSeeOther)
}

// explain turns common provider failures into a sentence that says what to fix.
func explain(err error, cfg config.Settings) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "HTTP 401"), strings.Contains(msg, "HTTP 403"):
		return "the provider rejected the API key. Check that it is complete and still active."
	case strings.Contains(msg, "HTTP 404"):
		return "the provider does not know the model \"" + cfg.AIModel + "\", or the API address is wrong."
	case strings.Contains(msg, "HTTP 429"):
		return "the provider is rate-limiting this key or the account is out of credit."
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"):
		return "could not reach " + cfg.AIBaseURL + ". Is the server running and the address right?"
	case strings.Contains(msg, "deadline exceeded"):
		return "the provider took longer than 90 seconds to answer."
	}
	if len(msg) > 240 {
		msg = msg[:240] + "..."
	}
	return msg
}

// Reconfigure rebuilds the sources and the drafter from the saved settings.
func (s *Server) Reconfigure(ctx context.Context) error {
	cfg, err := s.Config.Load(ctx)
	if err != nil {
		return err
	}
	accounts, err := s.Config.Accounts(ctx)
	if err != nil {
		return err
	}
	drafter, _ := cfg.Drafter()
	s.Monitor.Configure(config.Sources(accounts, config.XBudget{Store: s.Store, Cap: cfg.XCap()}), drafter)
	s.Monitor.ConfigureSearch(config.SearchClients(accounts))
	s.Monitor.ConfigureAnalytics(config.AnalyticsClients(accounts))
	s.Monitor.ConfigureScopes(config.Scopes(accounts))
	s.Monitor.ConfigureOwn(config.OwnNames(accounts))
	return nil
}

func (s *Server) addAccount(w http.ResponseWriter, r *http.Request) {
	platform := r.FormValue("platform")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	note, err := s.Config.AddAccountWithNote(ctx, platform, r.FormValue("name"), r.FormValue("secret"))
	if err != nil {
		http.Redirect(w, r, "/settings?account_error="+url.QueryEscape(err.Error())+"&open="+url.QueryEscape(platform)+"#accounts-"+url.PathEscape(platform), http.StatusSeeOther)
		return
	}
	if err := s.Reconfigure(ctx); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?added=1&note="+url.QueryEscape(note)+"&open="+url.QueryEscape(platform)+"#accounts-"+url.PathEscape(platform), http.StatusSeeOther)
}

func (s *Server) removeAccount(w http.ResponseWriter, r *http.Request) {
	if err := s.Config.RemoveAccount(r.Context(), id(r)); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Reconfigure(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings#accounts", http.StatusSeeOther)
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.base(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	saved, err := s.Store.SearchReport(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var report gsc.Report
	if saved.Data != "" {
		json.Unmarshal([]byte(saved.Data), &report)
	}
	// Opening the tab fetches fresh numbers, unless they are only minutes old
	// or this page is the result of a refresh.
	q := r.URL.Query()
	if time.Since(saved.FetchedAt) > freshFor(saved) && !q.Has("refreshed") && !q.Has("error") {
		s.Monitor.RefreshSearchInBackground(p)
	}
	data["Fetching"] = s.Monitor.SearchFetching(p.ID)
	if err := s.addPulse(ctx, p.ID, data); err != nil {
		s.fail(w, err)
		return
	}
	ctr, pos, weight := 0.0, 0.0, 0
	if report.Impressions > 0 {
		ctr = float64(report.Clicks) / float64(report.Impressions) * 100
	}
	for _, row := range report.Pages {
		pos += row.Position * float64(row.Impressions)
		weight += row.Impressions
	}
	if weight > 0 {
		pos /= float64(weight)
	}
	data["Title"], data["Tab"], data["Project"] = p.Name+" search", "search", p
	data["Report"], data["HasReport"], data["Saved"], data["Connected"] = report, saved.Data != "", saved, s.Monitor.HasSearch()
	data["CTR"], data["Position"], data["Refreshed"], data["RefreshError"] = ctr, pos, r.URL.Query().Has("refreshed"), r.URL.Query().Get("error")
	recs := insight.Analyze(report)
	data["CompareDay"] = r.URL.Query().Get("compare")
	if err := s.searchProgress(ctx, p.ID, report, data); err != nil {
		s.fail(w, err)
		return
	}
	data["Recs"], data["MoreRecs"] = recs[:min(len(recs), 8)], recs[min(len(recs), 8):]
	data["HasWriter"] = s.Monitor.Writer() != nil
	data["Plan"], _ = s.savedPlan(ctx, p.ID)
	data["PlanError"] = r.URL.Query().Get("plan_error")
	s.render(w, "search.html", data)
}

func (s *Server) refreshSearch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := "/p/" + strconv.FormatInt(p.ID, 10) + "/search"
	if _, err := s.Monitor.RefreshSearch(ctx, p); err != nil {
		http.Redirect(w, r, target+"?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, target+"?refreshed=1", http.StatusSeeOther)
}

// shownAccount is a connected account as the settings pages show it.
type shownAccount struct {
	ID       int64
	Platform string
	Name     string
	Prefix   string
	Hint     string
	FromEnv  bool
	All      bool           // used for every project
	In       map[int64]bool // the projects it is used for, when not all
	UsedFor  string         // "All projects" or the project names
	Here     bool           // used for the project being edited
}

func showAccount(a config.Account, projects []store.Project) shownAccount {
	pl, _ := config.PlatformByKey(a.Platform)
	hint := config.Hint(a.Secret)
	switch a.Platform {
	case "reddit":
		hint = "inbox feed saved" // the end of a feed link is the username, not a useful hint
	case "xaccount":
		hint = "uses the X API token"
	}
	sa := shownAccount{ID: a.ID, Platform: a.Platform, Name: a.Name, Prefix: pl.NamePrefix, Hint: hint, FromEnv: a.FromEnv, In: map[int64]bool{}}
	for _, id := range a.Projects {
		sa.In[id] = true
	}
	sa.All = len(a.Projects) == 0
	if sa.All {
		sa.UsedFor = "All projects"
		return sa
	}
	var names []string
	for _, p := range projects {
		if sa.In[p.ID] {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		sa.UsedFor = "No project"
	} else {
		sa.UsedFor = strings.Join(names, ", ")
	}
	return sa
}

// setAccountProjects saves which projects one of your accounts is used for.
func (s *Server) setAccountProjects(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var ids []int64
	if r.FormValue("scope") != "all" {
		for _, v := range r.Form["project"] {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, n)
			}
		}
		if len(ids) == 0 {
			ids = []int64{0} // used for no project: replies are not collected
		}
	}
	if err := s.Store.SetConnectionProjects(r.Context(), id(r), ids); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Reconfigure(r.Context()); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?scoped=1#mine", http.StatusSeeOther)
}

// saveAccountsForProject applies the "Your accounts" checkboxes of one project.
func (s *Server) saveAccountsForProject(r *http.Request, projectID int64) error {
	ctx := r.Context()
	accounts, err := s.Config.Accounts(ctx)
	if err != nil {
		return err
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	checked := map[string]bool{}
	for _, v := range r.Form["use_account"] {
		checked[v] = true
	}
	for _, a := range accounts {
		if a.FromEnv || config.PlatformKind(a.Platform) != config.KindAccount {
			continue
		}
		in := map[int64]bool{}
		if len(a.Projects) == 0 {
			for _, p := range projects {
				in[p.ID] = true
			}
		} else {
			for _, id := range a.Projects {
				in[id] = true
			}
		}
		want := checked[strconv.FormatInt(a.ID, 10)]
		if in[projectID] == want {
			continue
		}
		in[projectID] = want
		var ids []int64
		all := true
		for _, p := range projects {
			if in[p.ID] {
				ids = append(ids, p.ID)
			} else {
				all = false
			}
		}
		switch {
		case all:
			ids = nil // every project again
		case len(ids) == 0:
			ids = []int64{0}
		}
		if err := s.Store.SetConnectionProjects(ctx, a.ID, ids); err != nil {
			return err
		}
	}
	return s.Reconfigure(ctx)
}

// searchPlanStored is the AI plan saved for a project.
type searchPlanStored struct {
	At       time.Time `json:"at"`
	Text     string    `json:"text"`
	Provider string    `json:"provider"`
	Period   string    `json:"period"`
}

func planKey(projectID int64) string { return "search_plan_" + strconv.FormatInt(projectID, 10) }

func (s *Server) savedPlan(ctx context.Context, projectID int64) (*searchPlanStored, error) {
	saved, err := s.Store.Settings(ctx)
	if err != nil || saved[planKey(projectID)] == "" {
		return nil, err
	}
	var plan searchPlanStored
	if err := json.Unmarshal([]byte(saved[planKey(projectID)]), &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

const planInstructions = `You advise the team behind a product on how to get more people to it from Google search. You get their Google Search Console data for the last 28 days and the findings of an automatic analysis.

Write a short, prioritised plan: 5 to 8 numbered actions, most valuable first. Each action names the exact search and the page involved, says what to change or write, and says why in one sentence using the numbers given. Prefer actions that improve the product's own pages and content over generic SEO advice. Do not suggest buying links, keyword stuffing, or anything against Google's guidelines. Do not invent data you were not given. Plain text, no tables, no headings.`

func (s *Server) searchPlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := "/p/" + strconv.FormatInt(p.ID, 10) + "/search"
	writer := s.Monitor.Writer()
	if writer == nil {
		http.Redirect(w, r, target+"?plan_error="+url.QueryEscape("choose an AI provider in Settings first"), http.StatusSeeOther)
		return
	}
	saved, err := s.Store.SearchReport(ctx, p.ID)
	if err != nil || saved.Data == "" {
		http.Redirect(w, r, target+"?plan_error="+url.QueryEscape("fetch search data first"), http.StatusSeeOther)
		return
	}
	var report gsc.Report
	json.Unmarshal([]byte(saved.Data), &report)

	var b strings.Builder
	fmt.Fprintf(&b, "Product: %s (%s)\nPeriod: %s to %s. Clicks %d, impressions %d.\n\nTop searches (search | clicks | impressions | CTR %% | position):\n",
		p.Name, strings.Join(p.Domains, ", "), report.Start, report.End, report.Clicks, report.Impressions)
	for i, q := range report.Queries {
		if i == 40 {
			break
		}
		fmt.Fprintf(&b, "%s | %d | %d | %.1f | %.1f\n", q.Key, q.Clicks, q.Impressions, q.CTR, q.Position)
	}
	b.WriteString("\nTop pages (page | clicks | impressions | CTR % | position):\n")
	for i, pg := range report.Pages {
		if i == 20 {
			break
		}
		fmt.Fprintf(&b, "%s | %d | %d | %.1f | %.1f\n", pg.Key, pg.Clicks, pg.Impressions, pg.CTR, pg.Position)
	}
	b.WriteString("\nAutomatic findings:\n")
	for _, rec := range insight.Analyze(report) {
		fmt.Fprintf(&b, "- %s: %s. %s\n", rec.Label, rec.Title, rec.Detail)
	}
	text, err := writer.Write(ctx, planInstructions, b.String())
	if err != nil {
		cfg, _ := s.Config.Load(ctx)
		http.Redirect(w, r, target+"?plan_error="+url.QueryEscape(explain(err, cfg)), http.StatusSeeOther)
		return
	}
	cfg, _ := s.Config.Load(ctx)
	_, provider := cfg.Drafter()
	stored, _ := json.Marshal(searchPlanStored{At: time.Now().UTC(), Text: text, Provider: provider, Period: report.Start + " to " + report.End})
	if err := s.Store.SaveSettings(ctx, map[string]string{planKey(p.ID): string(stored)}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, target+"#plan", http.StatusSeeOther)
}

// activity tells an open inbox whether new mentions arrived or a check
// finished since it was loaded.
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.Activity(r.Context(), id(r), r.URL.Query().Get("since"))
	if err != nil {
		s.fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"latest_found": a.LatestFound, "last_check": a.LastCheck, "new": a.NewSince, "checking": s.Monitor.Checking()})
}

// searchFresh is how old search data can be before opening the Search tab
// fetches it again. Google updates it once a day, so this only stops repeat
// fetches when someone switches between tabs.
const searchFresh = 10 * time.Minute

// freshFor is how long a saved result stands before opening its tab tries
// again: after a failure, only a minute, since the owner may have just fixed
// the access the error asked for.
func freshFor(saved store.SearchReport) time.Duration {
	if saved.Error != "" {
		return time.Minute
	}
	return searchFresh
}

// searchStatus tells an open Search tab whether its fetch has finished.
func (s *Server) searchStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]bool{"fetching": s.Monitor.SearchFetching(id(r))})
}
