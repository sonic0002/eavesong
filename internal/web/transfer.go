package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sonic0002/eavesong/internal/store"
)

type portableProject struct {
	Name        string   `json:"name"`
	Domains     []string `json:"domains,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	Competitors []string `json:"competitors,omitempty"`
	OwnAccounts []string `json:"own_accounts,omitempty"`
	Description string   `json:"description,omitempty"`
	Tone        string   `json:"tone,omitempty"`
	ReplyLength string   `json:"reply_length,omitempty"`
	AvoidWords  string   `json:"avoid_words,omitempty"`
	Examples    string   `json:"examples,omitempty"`
	ReadLinks   bool     `json:"read_links,omitempty"`
}
type portableSettings struct {
	Version          int               `json:"version"`
	AIProvider       string            `json:"ai_provider"`
	AIModel          string            `json:"ai_model,omitempty"`
	XDailyCap        string            `json:"x_daily_cap,omitempty"`
	SMTPHost         string            `json:"smtp_host,omitempty"`
	SMTPPort         string            `json:"smtp_port,omitempty"`
	SMTPUser         string            `json:"smtp_user,omitempty"`
	SMTPFrom         string            `json:"smtp_from,omitempty"`
	SMTPTo           string            `json:"smtp_to,omitempty"`
	NotifyAttention  string            `json:"notify_attention,omitempty"`
	NotifyCompetitor string            `json:"notify_competitor,omitempty"`
	NotifySitemap    string            `json:"notify_sitemap,omitempty"`
	NotifyDaily      string            `json:"notify_daily,omitempty"`
	NotifyWeekly     string            `json:"notify_weekly,omitempty"`
	Projects         []portableProject `json:"projects"`
}

func (s *Server) exportSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg, err := s.Config.Load(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	// A custom AI address can contain credentials in its path or query. It is
	// intentionally excluded with keys and webhook URLs.
	out := portableSettings{Version: 1, AIProvider: cfg.AIProvider, AIModel: cfg.AIModel, XDailyCap: cfg.XDailyCap, SMTPHost: cfg.SMTPHost, SMTPPort: cfg.SMTPPort, SMTPUser: cfg.SMTPUser, SMTPFrom: cfg.SMTPFrom, SMTPTo: cfg.SMTPTo, NotifyAttention: cfg.NotifyAttention, NotifyCompetitor: cfg.NotifyCompetitor, NotifySitemap: cfg.NotifySitemap, NotifyDaily: cfg.NotifyDaily, NotifyWeekly: cfg.NotifyWeekly}
	for _, p := range projects {
		out.Projects = append(out.Projects, portableProject{Name: p.Name, Domains: p.Domains, Keywords: p.Keywords, Competitors: p.Competitors, OwnAccounts: p.OwnAccounts, Description: p.Description, Tone: p.Tone, ReplyLength: p.ReplyLength, AvoidWords: p.AvoidWords, Examples: p.Examples, ReadLinks: p.ReadLinks})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="eavesong-settings.json"`)
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(out)
}
func decodePortable(r io.Reader) (portableSettings, error) {
	var data portableSettings
	dec := json.NewDecoder(io.LimitReader(r, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&data); err != nil {
		return data, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return data, errors.New("file must contain one JSON object")
	}
	if data.Version != 1 {
		return data, fmt.Errorf("unsupported settings version %d", data.Version)
	}
	if len(data.Projects) > 100 {
		return data, errors.New("too many projects")
	}
	if cap, err := strconv.Atoi(data.XDailyCap); data.XDailyCap != "" && (err != nil || cap < 0) {
		return data, errors.New("daily X limit must be a non-negative whole number")
	}
	for _, flag := range []string{data.NotifyAttention, data.NotifyCompetitor, data.NotifySitemap, data.NotifyDaily, data.NotifyWeekly} {
		if flag != "" && flag != "1" {
			return data, errors.New("notification choices must be on or off")
		}
	}
	seen := map[string]bool{}
	for _, p := range data.Projects {
		name := strings.TrimSpace(p.Name)
		if name == "" || len(name) > 200 {
			return data, errors.New("each project needs a name of at most 200 characters")
		}
		if seen[strings.ToLower(name)] {
			return data, fmt.Errorf("duplicate project name %q", name)
		}
		seen[strings.ToLower(name)] = true
	}
	return data, nil
}
func (s *Server) importSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	file, _, err := r.FormFile("settings_file")
	if err != nil {
		http.Error(w, "Select an Eavesong settings JSON file.", 400)
		return
	}
	defer file.Close()
	data, err := decodePortable(file)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx := r.Context()
	current, err := s.Config.Load(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	next := current
	next.AIProvider, next.AIModel, next.XDailyCap = data.AIProvider, data.AIModel, data.XDailyCap
	if next.AIProvider != current.AIProvider {
		next.AIBaseURL = ""
	}
	next.SMTPHost, next.SMTPPort, next.SMTPUser, next.SMTPFrom, next.SMTPTo = data.SMTPHost, data.SMTPPort, data.SMTPUser, data.SMTPFrom, data.SMTPTo
	next.NotifyAttention, next.NotifyCompetitor, next.NotifySitemap, next.NotifyDaily, next.NotifyWeekly = data.NotifyAttention, data.NotifyCompetitor, data.NotifySitemap, data.NotifyDaily, data.NotifyWeekly
	if next.SMTPHost != current.SMTPHost || next.SMTPUser != current.SMTPUser {
		next.SMTPPassword = ""
	}
	// Imported files contain no credentials. Retain the current key only when
	// the provider has not changed; otherwise let the owner reconnect it.
	if next.AIProvider == current.AIProvider {
		next.AIKey = current.AIKey
	} else {
		next.AIKey = ""
	}
	switch next.AIProvider {
	case "none", "anthropic", "deepseek", "gemini", "openai":
	default:
		http.Error(w, "unknown AI provider in settings file", 400)
		return
	}
	existing, err := s.Store.Projects(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	byName := map[string]int64{}
	for _, p := range existing {
		byName[strings.ToLower(p.Name)] = p.ID
	}
	for _, p := range data.Projects {
		project := store.Project{ID: byName[strings.ToLower(p.Name)], Name: p.Name, Domains: p.Domains, Keywords: p.Keywords, Competitors: p.Competitors, OwnAccounts: p.OwnAccounts, DraftSettings: store.DraftSettings{Description: p.Description, Tone: p.Tone, ReplyLength: p.ReplyLength, AvoidWords: p.AvoidWords, Examples: p.Examples, ReadLinks: p.ReadLinks}}
		project.ID, err = s.Store.SaveProject(ctx, project)
		if err != nil {
			s.fail(w, err)
			return
		}
		if err := s.Store.RefreshOurs(ctx, project); err != nil {
			s.fail(w, err)
			return
		}
	}
	if err := s.Config.Save(ctx, next); err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Reconfigure(ctx); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?imported=1", http.StatusSeeOther)
}
