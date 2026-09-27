package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/budget"
	"github.com/JeremiahM37/lectern/v2/internal/outcomes"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// GET /api/usage/providers?days=N — usage per provider (docs/budgets.md
// "Provider usage"): spend and tokens by day for each agent CLI, the usage
// windows its logins last reported, a split by account where a CLI has more
// than one, and an estimated-cost table priced from the model price table.
// It reads the same usage_daily and attempts history as GET /api/usage.

type providerWindowView struct {
	budget.Window
	Account string `json:"account"`
	Target  string `json:"target"`
	Warn    bool   `json:"warn"`
}

type providerAccountView struct {
	Key          string               `json:"key"`
	AccountID    *int64               `json:"account_id,omitempty"`
	Label        string               `json:"label"`
	Target       string               `json:"target"`
	CostUSD      float64              `json:"cost_usd"`
	EstimatedUSD float64              `json:"estimated_usd"`
	InputTokens  int64                `json:"input_tokens"`
	OutputTokens int64                `json:"output_tokens"`
	Windows      []providerWindowView `json:"windows"`
	Warn         bool                 `json:"warn"`
}

type providerView struct {
	Agent        string                 `json:"agent"`
	Label        string                 `json:"label"`
	CostUSD      float64                `json:"cost_usd"`
	EstimatedUSD float64                `json:"estimated_usd"`
	InputTokens  int64                  `json:"input_tokens"`
	OutputTokens int64                  `json:"output_tokens"`
	Daily        []*usageDayBucket      `json:"daily"`
	Windows      []providerWindowView   `json:"windows"`
	Accounts     []*providerAccountView `json:"accounts"`
	Warn         bool                   `json:"warn"`
	// PeakPct is the fullest live window, for sorting and the card's meter.
	PeakPct float64 `json:"peak_pct"`
}

type costRow struct {
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	ReportedUSD  float64  `json:"reported_usd"`
	EstimatedUSD float64  `json:"estimated_usd"`
	ListUSD      *float64 `json:"list_usd,omitempty"`
	Priced       bool     `json:"priced"`
}

func (s *Server) providerUsage(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 366 {
			days = n
		}
	}
	writeJSON(w, 200, s.buildProviderUsage(days, time.Now()))
}

func (s *Server) buildProviderUsage(days int, now time.Time) map[string]any {
	cfg := budget.Load(s.DB)
	prices := outcomes.LoadPrices(s.DB)
	first := now.UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	providers := map[string]*providerView{}
	provider := func(agent string) *providerView {
		if agent == "" {
			agent = "claude"
		}
		p, ok := providers[agent]
		if !ok {
			p = &providerView{Agent: agent, Label: budget.ProviderLabel(agent), Windows: []providerWindowView{},
				Accounts: []*providerAccountView{}}
			providers[agent] = p
		}
		return p
	}
	for _, a := range []string{"claude", "codex", "gemini"} {
		provider(a)
	}
	dayOf := map[string]map[string]*usageDayBucket{}
	targetName := map[int64]string{}
	if ts, err := s.DB.Targets(); err == nil {
		for _, t := range ts {
			targetName[t.ID] = t.Name
		}
	}
	accountRows, _ := s.DB.Accounts()
	accountByID := map[int64]*store.Account{}
	for _, a := range accountRows {
		accountByID[a.ID] = a
	}
	// accountKey names the login a row ran under; NULL is the CLI's own.
	accounts := map[string]*providerAccountView{}
	accountFor := func(agent string, targetID int64, id *int64) *providerAccountView {
		key := fmt.Sprintf("%s:%d:default", agent, targetID)
		label := "Default"
		if id != nil {
			if a := accountByID[*id]; a != nil && !a.Default() {
				key, label = fmt.Sprintf("%s:%d:%d", agent, targetID, *id), a.Label
			}
		}
		v, ok := accounts[key]
		if !ok {
			v = &providerAccountView{Key: key, AccountID: id, Label: label, Target: targetName[targetID],
				Windows: []providerWindowView{}}
			accounts[key] = v
			p := provider(agent)
			p.Accounts = append(p.Accounts, v)
		}
		return v
	}
	costs := map[string]*costRow{}
	add := func(date, agent, model string, targetID *int64, account *int64, cost, estimated float64, in, out int64) {
		if date < first {
			return
		}
		p := provider(agent)
		p.CostUSD += cost
		p.EstimatedUSD += estimated
		p.InputTokens += in
		p.OutputTokens += out
		if dayOf[p.Agent] == nil {
			dayOf[p.Agent] = map[string]*usageDayBucket{}
		}
		d := dayOf[p.Agent][date]
		if d == nil {
			d = &usageDayBucket{Date: date}
			dayOf[p.Agent][date] = d
		}
		d.CostUSD += cost
		d.InputTokens += in
		d.OutputTokens += out
		if targetID != nil {
			a := accountFor(p.Agent, *targetID, account)
			a.CostUSD += cost
			a.EstimatedUSD += estimated
			a.InputTokens += in
			a.OutputTokens += out
		}
		key := p.Agent + "\x00" + model
		c := costs[key]
		if c == nil {
			c = &costRow{Agent: p.Agent, Model: model}
			costs[key] = c
		}
		c.InputTokens += in
		c.OutputTokens += out
		c.ReportedUSD += cost - estimated
		c.EstimatedUSD += estimated
	}

	if rows, err := s.DB.Query(`SELECT ud.date, ud.agent, ud.model, ud.cost_usd, ud.estimated_usd,
		ud.input_tokens, ud.output_tokens, sess.target_id, sess.account_id
		FROM usage_daily ud LEFT JOIN sessions sess ON sess.id = ud.session_id
		WHERE ud.session_id IS NOT NULL AND ud.date >= ?`, first); err == nil {
		for rows.Next() {
			var date, agent, model string
			var cost, est float64
			var in, out int64
			var target, account *int64
			if rows.Scan(&date, &agent, &model, &cost, &est, &in, &out, &target, &account) == nil {
				add(date, agent, model, target, account, cost, est, in, out)
			}
		}
		rows.Close()
	}
	cutoff := float64(now.AddDate(0, 0, -days).Unix())
	if rows, err := s.DB.Query(`SELECT a.finished_at, a.model, COALESCE(NULLIF(a.agent, ''), t.agent),
		p.target_id, a.account_id, a.result_json
		FROM attempts a JOIN tasks t ON t.id = a.task_id JOIN projects p ON p.id = t.project_id
		WHERE a.finished_at IS NOT NULL AND a.finished_at >= ? AND a.result_json != '{}'`, cutoff); err == nil {
		for rows.Next() {
			var finished float64
			var model, agent, raw string
			var target int64
			var account *int64
			if rows.Scan(&finished, &model, &agent, &target, &account, &raw) != nil {
				continue
			}
			result := store.UnjObj(raw)
			cost, _ := result["cost_usd"].(float64)
			est := 0.0
			if result["cost_source"] == "estimated" {
				est = cost
			}
			in, out := resultUsageTokens(result)
			tid := target
			add(time.Unix(int64(finished), 0).UTC().Format("2006-01-02"), agent, model, &tid, account, cost, est, in, out)
		}
		rows.Close()
	}

	warnAt := float64(cfg.UsageWarnPercent)
	for _, win := range budget.ProviderWindows(s.DB, now) {
		p := provider(win.Agent)
		a := accountFor(p.Agent, win.TargetID, win.AccountID)
		v := providerWindowView{Window: win, Account: a.Label, Target: targetName[win.TargetID], Warn: win.UsedPct >= warnAt}
		p.Windows = append(p.Windows, v)
		a.Windows = append(a.Windows, v)
		if v.Warn {
			p.Warn, a.Warn = true, true
		}
		if win.UsedPct > p.PeakPct {
			p.PeakPct = win.UsedPct
		}
	}

	out := make([]*providerView, 0, len(providers))
	for agent, p := range providers {
		// A dense series, oldest first, so every provider's chart lines up.
		for i := days - 1; i >= 0; i-- {
			date := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
			if d := dayOf[agent][date]; d != nil {
				p.Daily = append(p.Daily, d)
			} else {
				p.Daily = append(p.Daily, &usageDayBucket{Date: date})
			}
		}
		sort.Slice(p.Accounts, func(i, j int) bool { return p.Accounts[i].CostUSD > p.Accounts[j].CostUSD })
		out = append(out, p)
	}
	rank := map[string]int{"claude": 0, "codex": 1, "gemini": 2}
	sort.Slice(out, func(i, j int) bool {
		ri, iok := rank[out[i].Agent]
		rj, jok := rank[out[j].Agent]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		}
		return out[i].CostUSD > out[j].CostUSD
	})

	table := make([]*costRow, 0, len(costs))
	for _, c := range costs {
		if usd, ok := prices.EstimateFor(c.Agent, c.Model, c.InputTokens, c.OutputTokens); ok {
			c.ListUSD, c.Priced = &usd, true
		}
		table = append(table, c)
	}
	sort.Slice(table, func(i, j int) bool {
		return table[i].ReportedUSD+table[i].EstimatedUSD > table[j].ReportedUSD+table[j].EstimatedUSD
	})
	return map[string]any{"days": days, "warn_percent": cfg.UsageWarnPercent, "providers": out, "cost_table": table}
}
