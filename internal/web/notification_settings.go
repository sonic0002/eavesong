package web

import (
	"net/http"
	"net/url"
)

func validWebhook(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) && u.Host != "" && u.User == nil
}
func (s *Server) saveNotificationSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.Config.Load(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	cfg.SlackWebhook = keep(r, "slack_webhook", cfg.SlackWebhook)
	cfg.DiscordWebhook = keep(r, "discord_webhook", cfg.DiscordWebhook)
	cfg.GenericWebhook = keep(r, "generic_webhook", cfg.GenericWebhook)
	for _, p := range []string{cfg.SlackWebhook, cfg.DiscordWebhook, cfg.GenericWebhook} {
		if !validWebhook(p) {
			http.Error(w, "Webhook URLs must use HTTPS (or localhost HTTP).", 400)
			return
		}
	}
	check := func(name string) string {
		if r.FormValue(name) == "1" {
			return "1"
		}
		return ""
	}
	cfg.NotifyAttention = check("notify_attention")
	cfg.NotifyCompetitor = check("notify_competitor")
	cfg.NotifySitemap = check("notify_sitemap")
	cfg.NotifyDaily = check("notify_daily")
	cfg.NotifyWeekly = check("notify_weekly")
	if err := s.Config.Save(r.Context(), cfg); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings?notifications_saved=1#notifications", http.StatusSeeOther)
}
