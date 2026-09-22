package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/internal/push"
	"github.com/JeremiahM37/lectern/internal/sinks"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/version"
)

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{}
	rows, err := s.DB.Query(`SELECT status, COUNT(*) FROM tasks GROUP BY status`)
	if err == nil {
		for rows.Next() {
			var status string
			var n int
			if err := rows.Scan(&status, &n); err == nil {
				counts[status] = n
			}
		}
		rows.Close()
	}
	pending, _ := s.DB.Count("approvals", "status='pending'")
	// sessions are the other half of the board, and "how many agents are sitting
	// waiting for me" is the number a dashboard tile should lead with
	live, waiting := 0, 0
	if rows, err := s.DB.Sessions(false); err == nil {
		for _, row := range rows {
			live++
			if row.Status == "waiting" {
				waiting++
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"ok": true, "mock": s.Cfg.Mock, "version": version.Version, "build": version.Current(),
		"tasks":    counts,
		"sessions": live, "sessions_waiting": waiting,
		// flat, always-present counts so dashboard widgets (the Homepage
		// customapi tile) can map fields that never disappear when a column
		// empties out
		"running":           counts["running"],
		"queued":            counts["queued"],
		"review":            counts["review"],
		"pending_approvals": pending,
	})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.Notifier.Settings())
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	for key, value := range body {
		if !oneOf(key, sinks.Keys...) {
			httpError(w, 400, "unknown setting %q", key)
			return
		}
		str, ok := value.(string)
		if !ok {
			httpError(w, 400, "%s must be a string", key)
			return
		}
		if err := s.DB.SetSetting(key, strings.TrimSpace(str)); err != nil {
			respondErr(w, err)
			return
		}
	}
	writeJSON(w, 200, s.Notifier.Settings())
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	s.Notifier.Notify("Test notification", "lectern sinks are wired up 🎛", "/", nil)
	writeJSON(w, 200, map[string]any{"sent": true})
}

func (s *Server) getTemplates(w http.ResponseWriter, r *http.Request) {
	var out []map[string]any
	if raw := s.DB.Setting("templates"); raw != "" {
		json.Unmarshal([]byte(raw), &out)
	}
	if out == nil {
		out = []map[string]any{}
	}
	writeJSON(w, 200, out)
}

func (s *Server) putTemplates(w http.ResponseWriter, r *http.Request) {
	var body []map[string]any
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	for _, t := range body {
		name, ok := t["name"].(string)
		if !ok || name == "" {
			httpError(w, 400, "each template needs a name")
			return
		}
	}
	if body == nil {
		body = []map[string]any{}
	}
	if err := s.DB.SetSetting("templates", store.J(body)); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, body)
}

type projectCost struct {
	Name    string  `json:"name"`
	CostUSD float64 `json:"cost_usd"`
}

// stats aggregates what the board has actually spent. Cost is the one number
// that makes parallel dispatch a decision rather than a habit.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	weekAgo := store.Now() - 7*86400
	rows, err := s.DB.Query(`SELECT p.name, a.result_json, a.finished_at FROM attempts a
		JOIN tasks t ON t.id=a.task_id JOIN projects p ON p.id=t.project_id
		WHERE a.result_json != '{}'`)
	if err != nil {
		respondErr(w, err)
		return
	}
	var total, week float64
	byProject := map[string]float64{}
	for rows.Next() {
		var name, resultJSON string
		var finishedAt *float64
		if err := rows.Scan(&name, &resultJSON, &finishedAt); err != nil {
			continue
		}
		cost, _ := store.UnjObj(resultJSON)["cost_usd"].(float64)
		total += cost
		if finishedAt != nil && *finishedAt > weekAgo {
			week += cost
		}
		byProject[name] += cost
	}
	rows.Close()

	done, _ := s.DB.Count("tasks", "status='done'")
	list := make([]projectCost, 0, len(byProject))
	for name, cost := range byProject {
		list = append(list, projectCost{name, round4(cost)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CostUSD > list[j].CostUSD })
	writeJSON(w, 200, map[string]any{
		"total_cost_usd": round4(total), "last_7d_usd": round4(week),
		"tasks_done": done, "by_project": list,
	})
}

type janitorIn struct {
	Days *float64 `json:"days"`
}

func (s *Server) runJanitor(w http.ResponseWriter, r *http.Request) {
	var body janitorIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	days := s.Cfg.JanitorDays
	if body.Days != nil {
		days = *body.Days
	}
	out, err := s.Sched.Janitor(r.Context(), days)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) vapidKey(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.VAPIDPublicKey == "" {
		httpError(w, 404, "push not configured (set LECTERN_VAPID_PUBLIC/PRIVATE)")
		return
	}
	writeJSON(w, 200, map[string]any{"key": s.Cfg.VAPIDPublicKey})
}

func (s *Server) subscribePush(w http.ResponseWriter, r *http.Request) {
	var sub push.Subscription
	if err := decodeBody(r, &sub); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if sub.Endpoint == "" {
		httpError(w, 422, "endpoint is required")
		return
	}
	if err := sinks.Subscribe(s.DB, sub); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true})
}

func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}
