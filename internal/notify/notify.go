// Package notify sends optional read-only alerts about monitored mentions.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/store"
)

type Service struct {
	Store  *store.Store
	Config config.Manager
	Client *http.Client
	Now    func() time.Time
}
type Event struct{ Key, Kind, Project, Subject, Text, URL string }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) Run(ctx context.Context) error {
	cfg, err := s.Config.Load(ctx)
	if err != nil {
		return err
	}
	if cfg.SlackWebhook == "" && cfg.DiscordWebhook == "" && cfg.GenericWebhook == "" {
		return nil
	}
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, p := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		if cfg.NotifyAttention == "1" || cfg.NotifyCompetitor == "1" {
			mentions, err := s.Store.Mentions(ctx, p.ID, store.Filter{Status: "new", Limit: 5000})
			if err != nil {
				return err
			}
			for _, m := range mentions {
				if m.FoundAt.Before(now.AddDate(0, 0, -7)) || m.Ours {
					continue
				}
				kind := ""
				if m.Decision == "attention" && cfg.NotifyAttention == "1" {
					kind = "attention"
				}
				if m.About == "competitor" && cfg.NotifyCompetitor == "1" {
					if kind == "" {
						kind = "competitor"
					} else {
						kind = "attention and competitor"
					}
				}
				if kind == "" {
					continue
				}
				e := Event{Key: fmt.Sprintf("mention:%d", m.ID), Kind: kind, Project: p.Name, Subject: "Eavesong: " + p.Name + " mention", Text: fmt.Sprintf("%s mention on %s by %s\n%s\n%s", kind, m.Source, m.Author, m.Title, m.URL), URL: m.URL}
				if err := s.deliver(ctx, cfg, e); err != nil {
					return err
				}
			}
		}
		if cfg.NotifySitemap == "1" {
			saved, err := s.Store.SearchReport(ctx, p.ID)
			if err != nil {
				return err
			}
			if saved.Data != "" {
				var report gsc.Report
				if err := json.Unmarshal([]byte(saved.Data), &report); err != nil {
					return err
				}
				for _, sm := range report.Sitemaps {
					if sm.Errors == 0 {
						continue
					}
					e := Event{Key: fmt.Sprintf("sitemap:%d:%s:%s", p.ID, report.End, sm.Path), Kind: "sitemap", Project: p.Name, Subject: "Eavesong: sitemap errors for " + p.Name, Text: fmt.Sprintf("%s has %d sitemap errors in Search Console (%s).", sm.Path, sm.Errors, report.End), URL: sm.Path}
					if err := s.deliver(ctx, cfg, e); err != nil {
						return err
					}
				}
			}
		}
		if now.Hour() >= 8 && cfg.NotifyDaily == "1" {
			e, err := s.digest(ctx, p, 1, now)
			if err != nil {
				return err
			}
			e.Key = fmt.Sprintf("daily:%d:%s", p.ID, now.Format("2006-01-02"))
			e.Kind = "daily"
			if err := s.deliver(ctx, cfg, e); err != nil {
				return err
			}
		}
		if now.Weekday() == time.Monday && now.Hour() >= 8 && cfg.NotifyWeekly == "1" {
			e, err := s.digest(ctx, p, 7, now)
			if err != nil {
				return err
			}
			e.Key = fmt.Sprintf("weekly:%d:%s", p.ID, now.Format("2006-01-02"))
			e.Kind = "weekly"
			if err := s.deliver(ctx, cfg, e); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Service) digest(ctx context.Context, p store.Project, days int, now time.Time) (Event, error) {
	since := now.AddDate(0, 0, -days)
	mentions, err := s.Store.Mentions(ctx, p.ID, store.Filter{Limit: 5000})
	if err != nil {
		return Event{}, err
	}
	sources := map[string]int{}
	voice := map[string]int{}
	total, needs := 0, 0
	for _, m := range mentions {
		if m.PostedAt.Before(since) || m.Ours {
			continue
		}
		total++
		sources[m.Source]++
		if m.Decision == "attention" || m.Decision == "suggest" {
			needs++
		}
		for _, term := range m.Terms {
			voice[term]++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d mentions in the last %d days; %d needed a reply or personal look.\n", p.Name, total, days, needs)
	b.WriteString("By source:\n")
	for source, count := range sources {
		fmt.Fprintf(&b, "- %s: %d\n", source, count)
	}
	if days == 7 {
		b.WriteString("Share of voice:\n")
		for _, term := range append(append(append([]string{}, p.Domains...), p.Keywords...), p.Competitors...) {
			fmt.Fprintf(&b, "- %s: %d\n", term, voice[term])
		}
		saved, err := s.Store.SearchReport(ctx, p.ID)
		if err != nil {
			return Event{}, err
		}
		if saved.Data != "" {
			var report gsc.Report
			if json.Unmarshal([]byte(saved.Data), &report) == nil {
				fmt.Fprintf(&b, "Search Console (%s to %s): %d clicks, %d impressions.\n", report.Start, report.End, report.Clicks, report.Impressions)
			}
		}
		saved, err = s.Store.BingReport(ctx, p.ID)
		if err != nil {
			return Event{}, err
		}
		if saved.Data != "" {
			var report gsc.Report
			if json.Unmarshal([]byte(saved.Data), &report) == nil {
				fmt.Fprintf(&b, "Bing Webmaster Tools (%s to %s): %d clicks, %d impressions.\n", report.Start, report.End, report.Clicks, report.Impressions)
			}
		}
	}
	kind := "Daily digest"
	if days == 7 {
		kind = "Weekly report"
	}
	return Event{Project: p.Name, Subject: "Eavesong: " + kind + " for " + p.Name, Text: b.String()}, nil
}
func (s *Service) deliver(ctx context.Context, cfg config.Settings, e Event) error {
	type channel struct{ name, url string }
	channels := []channel{{"slack", cfg.SlackWebhook}, {"discord", cfg.DiscordWebhook}, {"webhook", cfg.GenericWebhook}}
	var failures []error
	for _, ch := range channels {
		if ch.url == "" {
			continue
		}
		sent, err := s.Store.NotificationDelivered(ctx, e.Key, ch.name)
		if err != nil {
			return err
		}
		if sent {
			continue
		}
		err = s.sendWebhook(ctx, ch.name, ch.url, e)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", ch.name, err))
			continue
		}
		if err := s.Store.RecordNotificationDelivery(ctx, e.Key, ch.name); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}
func (s *Service) sendWebhook(ctx context.Context, kind, endpoint string, e Event) error {
	payload := map[string]string{"event": e.Kind, "project": e.Project, "text": e.Text, "url": e.URL}
	if kind == "slack" {
		payload = map[string]string{"text": e.Text}
	}
	if kind == "discord" {
		payload = map[string]string{"content": e.Text}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
