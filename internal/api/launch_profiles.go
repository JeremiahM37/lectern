package api

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// launchProfilePresetsJSON is the shipped catalog of starting points for the
// profile form. It is data, not policy: choosing a preset only prefills the
// editable fields, and every preset is still saved as an ordinary profile.
//
//go:embed launch_profile_presets.json
var launchProfilePresetsJSON []byte

type launchProfilePreset struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Agent        string `json:"agent"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

func (s *Server) listLaunchProfilePresets(w http.ResponseWriter, r *http.Request) {
	var presets []launchProfilePreset
	if err := json.Unmarshal(launchProfilePresetsJSON, &presets); err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, presets)
}

func (s *Server) listLaunchProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.DB.LaunchProfiles()
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, profiles)
}

// launchProfileIn mirrors store.LaunchProfile, except the two briefing fields
// are pointers: a missing key means "an old client that does not know about
// this field, leave the stored value alone", while an explicit "" clears it.
type launchProfileIn struct {
	Name         string  `json:"name"`
	Agent        string  `json:"agent"`
	Command      string  `json:"command"`
	Model        string  `json:"model"`
	EnvJSON      string  `json:"env_json"`
	Description  *string `json:"description"`
	Instructions *string `json:"instructions"`
}

func (s *Server) saveLaunchProfile(w http.ResponseWriter, r *http.Request) {
	var in launchProfileIn
	if decodeBody(r, &in) != nil {
		httpError(w, 422, "invalid launch profile")
		return
	}
	p := store.LaunchProfile{Name: in.Name, Agent: in.Agent, Command: in.Command, Model: in.Model, EnvJSON: in.EnvJSON}
	if r.Method == http.MethodPut {
		id, err := pathID(r, "id")
		if err != nil {
			httpError(w, 422, "invalid launch profile id")
			return
		}
		p.ID = id
		// A legacy client that omits the briefing fields must not erase
		// instructions it never saw. Look up the stored profile so omitted
		// keys keep their current value; an explicit empty string still clears.
		existing, err := s.DB.LaunchProfile(id)
		if err != nil {
			respondErr(w, err)
			return
		}
		if in.Description == nil {
			p.Description = existing.Description
		}
		if in.Instructions == nil {
			p.Instructions = existing.Instructions
		}
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.Instructions != nil {
		p.Instructions = *in.Instructions
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
	if len(p.Description) > 2000 || strings.ContainsRune(p.Description, 0) {
		httpError(w, 422, "description must be at most 2000 bytes without NUL")
		return
	}
	if len(p.Instructions) > 16000 || strings.ContainsRune(p.Instructions, 0) {
		httpError(w, 422, "instructions must be at most 16000 bytes without NUL")
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
