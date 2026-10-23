package web

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sonic0002/eavesong/internal/ga"
)

// watchedRow is one place Eavesong listens, with the visits Google Analytics
// saw from it and the mentions Eavesong found there in the same days.
type watchedRow struct {
	Source   string
	Visits   int
	Mentions int
}

// traffic shows a project's Google Analytics data.
func (s *Server) traffic(w http.ResponseWriter, r *http.Request) {
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
	if err := s.addPulse(ctx, p.ID, data); err != nil {
		s.fail(w, err)
		return
	}
	saved, err := s.Store.AnalyticsReport(ctx, p.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	var report ga.Report
	if saved.Data != "" {
		json.Unmarshal([]byte(saved.Data), &report)
	}
	// Like the Search tab, opening it fetches fresh numbers unless they are
	// only minutes old or this page is the result of a refresh.
	q := r.URL.Query()
	if time.Since(saved.FetchedAt) > freshFor(saved) && !q.Has("refreshed") && !q.Has("error") {
		s.Monitor.RefreshAnalyticsInBackground(p)
	}
	data["Fetching"] = s.Monitor.AnalyticsFetching(p.ID)
	data["Title"], data["Tab"], data["Project"] = p.Name+" traffic", "traffic", p
	data["Report"], data["HasReport"], data["Saved"], data["Connected"] = report, saved.Data != "", saved, s.Monitor.HasAnalytics()
	data["Refreshed"], data["RefreshError"] = q.Has("refreshed"), q.Get("error")
	if saved.Data != "" {
		data["UsersChange"] = change(report.Users, report.PrevUsers)
		data["SessionsChange"] = change(report.Sessions, report.PrevSessions)
		data["ViewsChange"] = change(report.Views, report.PrevViews)
		data["Chart"] = dayChart(report)
		if data["Watched"], err = s.watched(ctx, p.ID, report); err != nil {
			s.fail(w, err)
			return
		}
	}
	s.render(w, "traffic.html", data)
}

// watched joins Analytics sources with mentions, per place Eavesong listens.
func (s *Server) watched(ctx context.Context, projectID int64, report ga.Report) ([]watchedRow, error) {
	mentions, err := s.Store.MentionsBySource(ctx, projectID, report.Start, report.End)
	if err != nil {
		return nil, err
	}
	rows := map[string]*watchedRow{}
	get := func(src string) *watchedRow {
		if rows[src] == nil {
			rows[src] = &watchedRow{Source: src}
		}
		return rows[src]
	}
	for _, r := range report.Sources {
		if src := ga.WatchedSource(r.Key); src != "" {
			get(src).Visits += r.Sessions
		}
	}
	for src, n := range mentions {
		if _, known := sourceNames[src]; known && src != "googlealerts" {
			get(src).Mentions += n
		}
	}
	out := make([]watchedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Visits != out[j].Visits {
			return out[i].Visits > out[j].Visits
		}
		if out[i].Mentions != out[j].Mentions {
			return out[i].Mentions > out[j].Mentions
		}
		return out[i].Source < out[j].Source
	})
	return out, nil
}

// change describes a figure against the 28 days before.
func change(now, before int) string {
	if before == 0 {
		return ""
	}
	pct := math.Round(float64(now-before) / float64(before) * 100)
	switch {
	case pct > 0:
		return fmt.Sprintf("up %.0f%% on the 28 days before", pct)
	case pct < 0:
		return fmt.Sprintf("down %.0f%% on the 28 days before", -pct)
	}
	return "same as the 28 days before"
}

// dayChart draws visitors per day as bars, one slot for every day of the
// period (Google leaves out days without visits), each with a tooltip.
func dayChart(report ga.Report) template.HTML {
	start, err1 := time.Parse("2006-01-02", report.Start)
	end, err2 := time.Parse("2006-01-02", report.End)
	if err1 != nil || err2 != nil || end.Before(start) {
		return ""
	}
	byDay := map[string]ga.Row{}
	for _, d := range report.Days {
		byDay[d.Key] = d
	}
	var days []ga.Row
	for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
		key := t.Format("2006-01-02")
		row, ok := byDay[key]
		if !ok {
			row = ga.Row{Key: key}
		}
		days = append(days, row)
	}
	const width, height, gap = 720, 120, 2
	max := 1
	for _, d := range days {
		if d.Users > max {
			max = d.Users
		}
	}
	barW := (float64(width) - float64(gap*(len(days)-1))) / float64(len(days))
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="day-chart" viewBox="0 0 %d %d" preserveAspectRatio="none" role="img" aria-label="Visitors per day">`, width, height+1)
	fmt.Fprintf(&b, `<line class="day-base" x1="0" y1="%d" x2="%d" y2="%d"/>`, height, width, height)
	for i, d := range days {
		h := float64(d.Users) / float64(max) * float64(height-6)
		if d.Users > 0 {
			h = math.Max(2, h)
		}
		x := float64(i) * (barW + gap)
		label := d.Key
		if t, err := time.Parse("2006-01-02", d.Key); err == nil {
			label = t.Format("Mon Jan 2")
		}
		fmt.Fprintf(&b, `<g class="day"><rect class="day-hit" x="%.1f" y="0" width="%.1f" height="%d"/><rect class="day-bar" x="%.1f" y="%.1f" width="%.1f" height="%.1f" rx="2"/><title>%s: %d visitors, %d sessions, %d page views</title></g>`,
			x, barW+gap, height, x, float64(height)-h, barW, h, template.HTMLEscapeString(label), d.Users, d.Sessions, d.Views)
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String())
}

func (s *Server) refreshTraffic(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := s.Store.Project(ctx, id(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := "/p/" + strconv.FormatInt(p.ID, 10) + "/traffic"
	if _, err := s.Monitor.RefreshAnalytics(ctx, p); err != nil {
		http.Redirect(w, r, target+"?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, target+"?refreshed=1", http.StatusSeeOther)
}

// trafficStatus tells an open Traffic tab whether its fetch has finished.
func (s *Server) trafficStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]bool{"fetching": s.Monitor.AnalyticsFetching(id(r))})
}
