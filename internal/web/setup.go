package web

import (
	"net/http"
	"strconv"
)

func (s *Server) setupNext(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.Settings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if settings["setup_stage"] != "accounts" {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if err := s.Store.SaveSettings(r.Context(), map[string]string{"setup_stage": "ai"}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/settings#drafts", http.StatusSeeOther)
}
func (s *Server) setupFinish(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.Settings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if settings["setup_stage"] != "ai" {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	projects, err := s.Store.Projects(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(projects) == 0 {
		http.Redirect(w, r, "/settings/0", http.StatusSeeOther)
		return
	}
	if err := s.Store.SaveSettings(r.Context(), map[string]string{"setup_stage": ""}); err != nil {
		s.fail(w, err)
		return
	}
	http.Redirect(w, r, "/p/"+strconv.FormatInt(projects[0].ID, 10), http.StatusSeeOther)
}
