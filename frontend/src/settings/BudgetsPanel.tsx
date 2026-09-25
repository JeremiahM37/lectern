// Settings → Budgets: daily/weekly USD caps (overall and per agent), mode
// (warn/stop) and alert thresholds — see docs/budgets.md. Backed by
// GET/PUT /api/budgets (internal/api/budgets.go).
import { useEffect, useState } from "react";
import type { BudgetConfig, BudgetLimit, BudgetStatus } from "../types";

export interface BudgetsPanelApi {
  request<T>(p: string, o?: { method?: string; body?: unknown }): Promise<T>;
}

const emptyLimit: BudgetLimit = { daily_usd: 0, weekly_usd: 0, mode: "warn" };

function numOrZero(v: string): number {
  const n = Number(v);
  return Number.isFinite(n) && n >= 0 ? n : 0;
}

export function BudgetsPanel({ api, onNotice }: { api: BudgetsPanelApi; onNotice(t: string, e?: boolean): void }) {
  const [status, setStatus] = useState<BudgetStatus>();
  const [overall, setOverall] = useState<BudgetLimit>(emptyLimit);
  const [perAgent, setPerAgent] = useState<[string, BudgetLimit][]>([]);
  const [thresholds, setThresholds] = useState("75,90,100");
  const [quotaThresholds, setQuotaThresholds] = useState("75,90");
  const [anomalyEnabled, setAnomalyEnabled] = useState(true);
  const [anomalyMultiplier, setAnomalyMultiplier] = useState("3");

  function applyConfig(cfg: BudgetConfig) {
    setOverall(cfg.overall);
    setPerAgent(Object.entries(cfg.per_agent));
    setThresholds(cfg.thresholds.join(","));
    setQuotaThresholds(cfg.quota_thresholds.join(","));
    setAnomalyEnabled(cfg.anomaly_enabled);
    setAnomalyMultiplier(String(cfg.anomaly_multiplier));
  }

  function load() {
    void api
      .request<BudgetStatus>("/budgets")
      .then((s) => {
        setStatus(s);
        applyConfig(s.config);
      })
      .catch((e) => onNotice(String(e), true));
  }
  useEffect(load, []);

  function parseIntList(s: string): number[] {
    return s
      .split(",")
      .map((v) => Number(v.trim()))
      .filter((n) => Number.isFinite(n) && n > 0 && n <= 100);
  }

  function save() {
    const body: BudgetConfig = {
      overall,
      per_agent: Object.fromEntries(perAgent.filter(([name]) => name.trim() !== "")),
      thresholds: parseIntList(thresholds),
      quota_thresholds: parseIntList(quotaThresholds),
      anomaly_enabled: anomalyEnabled,
      anomaly_multiplier: numOrZero(anomalyMultiplier) || 3,
    };
    void api
      .request<BudgetStatus>("/budgets", { method: "PUT", body })
      .then((s) => {
        setStatus(s);
        applyConfig(s.config);
        onNotice("Budgets saved");
      })
      .catch((e) => onNotice(String(e), true));
  }

  // Gate the editable form on the initial fetch having landed: mounting the
  // inputs immediately (with placeholder/default state) and applying the
  // fetch response on top of whatever the operator may have already typed
  // is a real race — a fast typist (or, as found in e2e, Playwright's own
  // speed) can have their first edit silently overwritten by a GET that
  // resolves a moment later. A brief "Loading budgets…" avoids the window
  // entirely rather than trying to merge concurrent writes.
  if (!status) {
    return (
      <article id="budgets-panel" className="budgets-editor">
        <h3>Budgets</h3>
        <p>Loading budgets…</p>
      </article>
    );
  }

  return (
    <article id="budgets-panel" className="budgets-editor">
      <h3>Budgets</h3>
      <p className="subhint">
        Daily and weekly USD spend caps, overall and per agent, plus a per-task budget (set on the task
        itself). "Warn" only alerts; "Stop" refuses new dispatches and session launches once a limit
        reaches 100%, and cancels a task over its own budget — an interactive session is never killed,
        only told.
      </p>

      <fieldset>
        <legend>Overall</legend>
        <div className="limit-row">
          <label>
            Daily cap (USD)
            <input
              id="budget-overall-daily"
              type="number"
              min="0"
              step="0.01"
              value={overall.daily_usd || ""}
              onChange={(e) => { const v = numOrZero(e.target.value); setOverall((prev) => ({ ...prev, daily_usd: v })); }}
              placeholder="no cap"
            />
          </label>
          <label>
            Weekly cap (USD)
            <input
              id="budget-overall-weekly"
              type="number"
              min="0"
              step="0.01"
              value={overall.weekly_usd || ""}
              onChange={(e) => { const v = numOrZero(e.target.value); setOverall((prev) => ({ ...prev, weekly_usd: v })); }}
              placeholder="no cap"
            />
          </label>
          <label>
            Mode
            <select
              id="budget-overall-mode"
              value={overall.mode}
              onChange={(e) => { const v = e.target.value as BudgetLimit["mode"]; setOverall((prev) => ({ ...prev, mode: v })); }}
            >
              <option value="warn">Warn only</option>
              <option value="stop">Stop at 100%</option>
            </select>
          </label>
        </div>
        {status?.overall && (status.overall.daily?.blocked || status.overall.weekly?.blocked) && (
          <p className="budget-blocked-note">The overall budget is currently exhausted in stop mode.</p>
        )}
      </fieldset>

      <fieldset>
        <legend>Per agent</legend>
        {perAgent.map(([name, limit], i) => (
          <div className="per-agent-row" key={i}>
            <input
              placeholder="agent name (e.g. claude, codex)"
              value={name}
              onChange={(e) => {
                const val = e.target.value;
                setPerAgent((list) => list.map((row, j) => (j === i ? [val, row[1]] : row)));
              }}
            />
            <input
              type="number"
              min="0"
              step="0.01"
              placeholder="daily $"
              value={limit.daily_usd || ""}
              onChange={(e) => {
                const val = numOrZero(e.target.value);
                setPerAgent((list) => list.map((row, j) => (j === i ? [row[0], { ...row[1], daily_usd: val }] : row)));
              }}
            />
            <input
              type="number"
              min="0"
              step="0.01"
              placeholder="weekly $"
              value={limit.weekly_usd || ""}
              onChange={(e) => {
                const val = numOrZero(e.target.value);
                setPerAgent((list) => list.map((row, j) => (j === i ? [row[0], { ...row[1], weekly_usd: val }] : row)));
              }}
            />
            <select
              value={limit.mode}
              onChange={(e) => {
                const val = e.target.value as BudgetLimit["mode"];
                setPerAgent((list) => list.map((row, j) => (j === i ? [row[0], { ...row[1], mode: val }] : row)));
              }}
            >
              <option value="warn">Warn</option>
              <option value="stop">Stop</option>
            </select>
            <button className="b" onClick={() => setPerAgent((list) => list.filter((_, j) => j !== i))}>
              ✕
            </button>
          </div>
        ))}
        <button
          className="b"
          id="budget-add-agent"
          onClick={() => setPerAgent((list) => [...list, ["", { ...emptyLimit }]])}
        >
          + Add agent limit
        </button>
      </fieldset>

      <fieldset>
        <legend>Alerts</legend>
        <label>
          Spend thresholds (%)
          <input value={thresholds} onChange={(e) => setThresholds(e.target.value)} placeholder="75,90,100" />
        </label>
        <label>
          Claude quota thresholds (%)
          <input value={quotaThresholds} onChange={(e) => setQuotaThresholds(e.target.value)} placeholder="75,90" />
        </label>
        <label>
          <input type="checkbox" checked={anomalyEnabled} onChange={(e) => setAnomalyEnabled(e.target.checked)} />{" "}
          Cost anomaly detection
        </label>
        <label>
          Anomaly multiplier (x trailing 7-day median $/hour)
          <input
            value={anomalyMultiplier}
            onChange={(e) => setAnomalyMultiplier(e.target.value)}
            disabled={!anomalyEnabled}
          />
        </label>
      </fieldset>

      <button id="budget-save" onClick={save}>
        Save budgets
      </button>
    </article>
  );
}
