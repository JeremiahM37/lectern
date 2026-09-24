package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// usageDayBucket is one day's combined session+task spend.
type usageDayBucket struct {
	Date         string  `json:"date"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

type usageSplit struct {
	Key          string  `json:"key"`
	Agent        string  `json:"agent,omitempty"`
	Model        string  `json:"model,omitempty"`
	ProjectID    *int64  `json:"project_id,omitempty"`
	ProjectName  string  `json:"project_name,omitempty"`
	CostUSD      float64 `json:"cost_usd"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
}

type usageTopSession struct {
	ID      int64   `json:"id"`
	Name    string  `json:"name"`
	Agent   string  `json:"agent"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"cost_usd"`
}

type usageTopTask struct {
	ID      int64   `json:"id"`
	Title   string  `json:"title"`
	CostUSD float64 `json:"cost_usd"`
}

type usageRateWindow struct {
	UsedPercentage int     `json:"used_percentage"`
	ResetsAt       float64 `json:"resets_at"`
}

type usageQuota struct {
	FiveHour usageRateWindow `json:"five_hour"`
	SevenDay usageRateWindow `json:"seven_day"`
	At       float64         `json:"at"`
	// Stale is true when the account-wide rate_limits snapshot (last written
	// by any session's statusline — see internal/agentevents.IngestStatusline)
	// is more than 30 minutes old. A dead session's numbers do not update, so
	// staleness has to be surfaced rather than shown as if it were live.
	Stale bool `json:"stale"`
	// Empty is true when no statusline has ever reported rate limits, so the
	// frontend can render "no data" instead of a misleading 0%.
	Empty bool `json:"empty"`
}

// usageReport is GET /api/usage?days=N's response: totals by day, split by
// agent/model and by project, today's/this week's combined spend, top
// sessions/tasks by cost, and the account-wide quota panel.
//
// Day-bucketed history comes from usage_daily, which today only the session
// hook path (internal/agentevents) writes — see its schema comment. Task
// spend has no per-day granularity of its own (a task's cost is a single
// terminal number on its latest attempt, not a series of ticks like a
// session's statusline), so it is read straight from attempts.result_json,
// bucketed by the attempt's finish date, and merged into the same day/
// agent-model/project totals. This mirrors the existing GET /api/stats
// (all-time task cost by project) rather than replacing it.
func (s *Server) usageReport(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 366 {
			days = n
		}
	}
	now := store.Now()
	cutoff := now - float64(days)*86400
	cutoffDate := time.Unix(int64(cutoff), 0).UTC().Format("2006-01-02")
	todayDate := time.Now().UTC().Format("2006-01-02")
	weekCutoff := now - 7*86400

	projects, _ := s.DB.Projects()
	projectName := map[int64]string{}
	for _, p := range projects {
		projectName[p.ID] = p.Name
	}

	byDay := map[string]*usageDayBucket{}
	dayAt := func(date string) *usageDayBucket {
		b, ok := byDay[date]
		if !ok {
			b = &usageDayBucket{Date: date}
			byDay[date] = b
		}
		return b
	}
	byAgentModel := map[string]*usageSplit{}
	byProject := map[string]*usageSplit{}
	addUsage := func(date, agent, model string, projectID *int64, cost float64, in, out int64) {
		if date < cutoffDate {
			return
		}
		d := dayAt(date)
		d.CostUSD += cost
		d.InputTokens += in
		d.OutputTokens += out

		amKey := agent + "\x00" + model
		am, ok := byAgentModel[amKey]
		if !ok {
			am = &usageSplit{Key: amKey, Agent: agent, Model: model}
			byAgentModel[amKey] = am
		}
		am.CostUSD += cost
		am.InputTokens += in
		am.OutputTokens += out

		pKey := "none"
		if projectID != nil {
			pKey = strconv.FormatInt(*projectID, 10)
		}
		pr, ok := byProject[pKey]
		if !ok {
			pr = &usageSplit{Key: pKey, ProjectID: projectID}
			if projectID != nil {
				pr.ProjectName = projectName[*projectID]
			}
			byProject[pKey] = pr
		}
		pr.CostUSD += cost
		pr.InputTokens += in
		pr.OutputTokens += out
	}

	var todayUSD, weekUSD float64
	addSpend := func(date string, ts float64, cost float64) {
		if date == todayDate {
			todayUSD += cost
		}
		if ts >= weekCutoff {
			weekUSD += cost
		}
	}

	// Session usage: usage_daily rows are already deltas, one per (date,
	// session, agent, model) — see internal/store/usage.go.
	rows, err := s.DB.Query(`SELECT ud.date, ud.agent, ud.model, ud.cost_usd,
		ud.input_tokens, ud.output_tokens, sess.project_id
		FROM usage_daily ud LEFT JOIN sessions sess ON sess.id = ud.session_id
		WHERE ud.session_id IS NOT NULL AND ud.date >= ?`, cutoffDate)
	if err != nil {
		respondErr(w, err)
		return
	}
	for rows.Next() {
		var date, agent, model string
		var cost float64
		var in, out int64
		var projectID *int64
		if err := rows.Scan(&date, &agent, &model, &cost, &in, &out, &projectID); err != nil {
			continue
		}
		addUsage(date, agent, model, projectID, cost, in, out)
		// usage_daily has no per-row timestamp, only a date bucket, so
		// today/week spend approximates "today" and "this week" by date
		// rather than exact seconds — fine at day granularity.
		if date == todayDate {
			todayUSD += cost
		}
		if d, err := time.Parse("2006-01-02", date); err == nil && d.Unix() >= int64(weekCutoff)-86400 {
			weekUSD += cost
		}
	}
	rows.Close()

	// Task usage: no usage_daily rows yet (docs/agent-events.md: "today only
	// the session ingest path writes it"), so read attempts.result_json
	// directly, bucketed by the attempt's finish time.
	rows, err = s.DB.Query(`SELECT a.finished_at, a.model, t.agent, t.project_id, a.result_json
		FROM attempts a JOIN tasks t ON t.id = a.task_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ? AND a.result_json != '{}'`, cutoff)
	if err != nil {
		respondErr(w, err)
		return
	}
	for rows.Next() {
		var finishedAt float64
		var model, agent string
		var projectID int64
		var resultJSON string
		if err := rows.Scan(&finishedAt, &model, &agent, &projectID, &resultJSON); err != nil {
			continue
		}
		result := store.UnjObj(resultJSON)
		cost, _ := result["cost_usd"].(float64)
		in, out := resultUsageTokens(result)
		date := time.Unix(int64(finishedAt), 0).UTC().Format("2006-01-02")
		pid := projectID
		addUsage(date, agent, model, &pid, cost, in, out)
		addSpend(date, finishedAt, cost)
	}
	rows.Close()

	daily := make([]*usageDayBucket, 0, len(byDay))
	for _, b := range byDay {
		daily = append(daily, b)
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].Date < daily[j].Date })

	agentModel := make([]*usageSplit, 0, len(byAgentModel))
	for _, v := range byAgentModel {
		agentModel = append(agentModel, v)
	}
	sort.Slice(agentModel, func(i, j int) bool { return agentModel[i].CostUSD > agentModel[j].CostUSD })

	project := make([]*usageSplit, 0, len(byProject))
	for _, v := range byProject {
		project = append(project, v)
	}
	sort.Slice(project, func(i, j int) bool { return project[i].CostUSD > project[j].CostUSD })

	topSessions := []usageTopSession{}
	if rows, err := s.DB.Query(`SELECT id, name, agent, model, cost_usd FROM sessions
		WHERE cost_usd IS NOT NULL ORDER BY cost_usd DESC LIMIT 10`); err == nil {
		for rows.Next() {
			var t usageTopSession
			if rows.Scan(&t.ID, &t.Name, &t.Agent, &t.Model, &t.CostUSD) == nil {
				topSessions = append(topSessions, t)
			}
		}
		rows.Close()
	}

	taskCost := map[int64]*usageTopTask{}
	if rows, err := s.DB.Query(`SELECT t.id, t.title, a.result_json FROM attempts a
		JOIN tasks t ON t.id = a.task_id WHERE a.result_json != '{}'`); err == nil {
		for rows.Next() {
			var id int64
			var title, resultJSON string
			if rows.Scan(&id, &title, &resultJSON) != nil {
				continue
			}
			cost, _ := store.UnjObj(resultJSON)["cost_usd"].(float64)
			tt, ok := taskCost[id]
			if !ok {
				tt = &usageTopTask{ID: id, Title: title}
				taskCost[id] = tt
			}
			tt.CostUSD += cost
		}
		rows.Close()
	}
	topTasks := make([]usageTopTask, 0, len(taskCost))
	for _, t := range taskCost {
		topTasks = append(topTasks, *t)
	}
	sort.Slice(topTasks, func(i, j int) bool { return topTasks[i].CostUSD > topTasks[j].CostUSD })
	if len(topTasks) > 10 {
		topTasks = topTasks[:10]
	}

	writeJSON(w, 200, map[string]any{
		"days":           days,
		"generated_at":   now,
		"daily":          daily,
		"by_agent_model": agentModel,
		"by_project":     project,
		"today_usd":      round4(todayUSD),
		"week_usd":       round4(weekUSD),
		"top_sessions":   topSessions,
		"top_tasks":      topTasks,
		"quota":          s.usageQuota(now),
	})
}

// resultUsageTokens pulls whatever token counts an attempt's result payload
// reports, tolerating the different shapes NormalizeClaude/normalizeCodex
// produce (see internal/agents/parse.go) — a plain "usage" object (Claude),
// or the flatter context_tokens/output_tokens this pass added.
func resultUsageTokens(result map[string]any) (input, output int64) {
	if usage, ok := result["usage"].(map[string]any); ok {
		in, _ := usage["input_tokens"].(float64)
		cc, _ := usage["cache_creation_input_tokens"].(float64)
		cr, _ := usage["cache_read_input_tokens"].(float64)
		out, _ := usage["output_tokens"].(float64)
		return int64(in + cc + cr), int64(out)
	}
	if ct, ok := result["context_tokens"].(float64); ok {
		out, _ := result["output_tokens"].(float64)
		return int64(ct), int64(out)
	}
	return 0, 0
}

// usageQuota reads the account-wide rate_limits setting (written by
// internal/agentevents.IngestStatusline on every Claude statusline tick) for
// the always-visible quota chip.
func (s *Server) usageQuota(now float64) usageQuota {
	raw := s.DB.Setting("rate_limits")
	if raw == "" {
		return usageQuota{Empty: true}
	}
	obj := store.UnjObj(raw)
	get := func(key string) usageRateWindow {
		w, _ := obj[key].(map[string]any)
		pct, _ := w["used_percentage"].(float64)
		resets, _ := w["resets_at"].(float64)
		return usageRateWindow{UsedPercentage: int(pct), ResetsAt: resets}
	}
	at, _ := obj["at"].(float64)
	return usageQuota{
		FiveHour: get("five_hour"),
		SevenDay: get("seven_day"),
		At:       at,
		Stale:    at > 0 && now-at > 1800,
	}
}
