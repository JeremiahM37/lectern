package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func (s *Server) listLaunchProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.DB.LaunchProfiles()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, profiles)
}

func (s *Server) saveLaunchProfile(w http.ResponseWriter, r *http.Request) {
	var p store.LaunchProfile
	if decodeBody(r, &p) != nil {
		httpError(w, 422, "invalid launch profile")
		return
	}
	p.ID = 0
	if r.Method == http.MethodPut {
		id, err := pathID(r, "id")
		if err != nil {
			httpError(w, 422, "invalid launch profile id")
			return
		}
		p.ID = id
	}
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" || len(p.Name) > 120 || strings.IndexFunc(p.Name, unicode.IsControl) >= 0 {
		httpError(w, 422, "profile name must contain 1–120 bytes without control characters")
		return
	}
	if _, ok := sessions.Find(s.agentSpecs(), p.Agent); !ok {
		httpError(w, 422, "choose an installed agent definition")
		return
	}
	if len(p.Command) > 4096 || len(p.Model) > 256 || strings.ContainsRune(p.Command+p.Model, 0) {
		httpError(w, 422, "invalid command or model")
		return
	}
	if p.EnvJSON == "" {
		p.EnvJSON = "{}"
	}
	var env map[string]string
	if len(p.EnvJSON) > 128<<10 || json.Unmarshal([]byte(p.EnvJSON), &env) != nil || env == nil {
		httpError(w, 422, "environment must be a JSON object of string values")
		return
	}
	if _, err := sessions.EnvPrefix(env); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	for _, value := range env {
		if strings.ContainsRune(value, 0) {
			httpError(w, 422, "environment values cannot contain NUL")
			return
		}
	}
	p.EnvJSON = store.J(env)
	saved, err := s.DB.SaveLaunchProfile(&p)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			httpError(w, 409, "a launch profile already uses that name")
		} else {
			respondErr(w, err)
		}
		return
	}
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, saved)
}

func (s *Server) deleteLaunchProfile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 422, "invalid launch profile id")
		return
	}
	if err := s.DB.DeleteLaunchProfile(id); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
