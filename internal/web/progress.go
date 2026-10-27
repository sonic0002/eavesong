package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/sonic0002/eavesong/internal/gsc"
	"github.com/sonic0002/eavesong/internal/store"
)

func (s *Server) draftUsage(w http.ResponseWriter, r *http.Request) {
	event := r.FormValue("event")
	if err := s.Store.RecordDraftUsage(r.Context(), id(r), event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if event == "copied" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	m, err := s.Store.Mention(r.Context(), id(r))
	if err != nil {
		s.fail(w, err)
		return
	}
	back(w, r, "/p/"+strconv.FormatInt(m.ProjectID, 10))
}
func (s *Server) recommendation(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Store.Project(r.Context(), id(r)); err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.SetRecommendationAction(r.Context(), id(r), r.FormValue("key"), r.FormValue("status")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	engine := ""
	if strings.HasPrefix(r.FormValue("key"), "bing:") {
		engine = "bing"
	}
	http.Redirect(w, r, "/p/"+strconv.FormatInt(id(r), 10)+"/search"+engineQuery(engine), http.StatusSeeOther)
}
func (s *Server) searchProgress(ctx context.Context, engine string, projectID int64, report gsc.Report, data map[string]any) error {
	all, err := s.Store.RecommendationActions(ctx, projectID)
	if err != nil {
		return err
	}
	states := map[string]string{}
	var actions []store.RecommendationAction
	for _, a := range all {
		if strings.HasPrefix(a.Key, "bing:") != (engine == "bing") {
			continue // the other engine's follow-through
		}
		states[a.Key] = a.Status
		actions = append(actions, a)
	}
	data["RecStates"], data["RecActions"] = states, actions
	history, err := s.Store.SearchHistory(ctx, projectID)
	if engine == "bing" {
		history, err = s.Store.BingHistory(ctx, projectID)
	}
	if err != nil {
		return err
	}
	data["History"] = history
	var chosen *store.SearchSnapshot
	day, _ := data["CompareDay"].(string)
	for i := range history {
		if history[i].Day == day {
			chosen = &history[i]
			break
		}
	}
	if chosen == nil && len(history) > 1 {
		chosen = &history[len(history)-1]
	}
	if chosen != nil && chosen.Report.Property == report.Property {
		data["Comparison"] = chosen
		data["ClickChange"] = report.Clicks - chosen.Report.Clicks
		data["ImpressionChange"] = report.Impressions - chosen.Report.Impressions
	}
	return nil
}
