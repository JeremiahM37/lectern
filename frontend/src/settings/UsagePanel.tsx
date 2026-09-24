// The Usage view (Settings → "Usage & about"): totals by day for the last
// N days, split by agent/model and by project, today's/this week's spend,
// top sessions/tasks by cost, and the account-wide quota panel. Backed by
// GET /api/usage?days=N (internal/api/usage.go) — see its doc comment for
// what "combined session+task spend" means.
import { useEffect, useState } from "react";
import type { UsageReport } from "../types";
import { formatAge, formatCost, formatCountdown, formatTokens, quotaClass } from "../sessions/usageFormat";

export interface UsagePanelApi {
  request<T>(path: string): Promise<T>;
}

export function UsagePanel({ api }: { api: UsagePanelApi }) {
  const [days, setDays] = useState(30);
  const [report, setReport] = useState<UsageReport>();
  const [error, setError] = useState("");
  useEffect(() => {
    let cancelled = false;
    api
      .request<UsageReport>(`/usage?days=${days}`)
      .then((r) => {
        if (!cancelled) setReport(r);
      })
      .catch((e) => !cancelled && setError(String(e)));
    return () => {
      cancelled = true;
    };
  }, [api, days]);
  if (error) return <p className="usage-error">{error}</p>;
  if (!report) return <p>Loading usage…</p>;
  const maxDay = Math.max(0.0001, ...report.daily.map((d) => d.cost_usd));
  const quota = report.quota;
  return (
    <div className="usage-panel">
      {!quota.empty && (
        <section className="usage-quota" aria-label="Account quota">
          <h4>Account quota</h4>
          <div className={`usage-quota-rows${quota.stale ? " stale" : ""}`}>
            <QuotaRow label="5 hour" window={quota.five_hour} />
            <QuotaRow label="7 day" window={quota.seven_day} />
          </div>
          {quota.stale && <p className="sub">Last updated {formatAge(quota.at)} — a session's statusline hasn't reported since.</p>}
        </section>
      )}
      <section className="usage-totals">
        <div className="usage-stat">
          <b>{formatCost(report.today_usd)}</b>
          <span>today</span>
        </div>
        <div className="usage-stat">
          <b>{formatCost(report.week_usd)}</b>
          <span>this week</span>
        </div>
        <label className="usage-days">
          Window{" "}
          <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {[7, 14, 30, 90].map((n) => (
              <option key={n} value={n}>
                {n} days
              </option>
            ))}
          </select>
        </label>
      </section>
      <section className="usage-daily-chart" aria-label="Cost by day">
        <h4>Cost by day</h4>
        <div className="usage-bars">
          {report.daily.map((d) => (
            <div className="usage-bar" key={d.date} title={`${d.date}: ${formatCost(d.cost_usd)}`}>
              <i style={{ height: `${Math.max(2, (d.cost_usd / maxDay) * 100)}%` }} />
            </div>
          ))}
          {report.daily.length === 0 && <p className="sub">No usage recorded in this window.</p>}
        </div>
      </section>
      <div className="usage-split-grid">
        <section aria-label="By agent and model">
          <h4>By agent / model</h4>
          <table className="usage-table">
            <tbody>
              {report.by_agent_model.map((row) => (
                <tr key={row.key}>
                  <td>
                    {row.agent}
                    {row.model ? ` · ${row.model}` : ""}
                  </td>
                  <td>{formatCost(row.cost_usd)}</td>
                  <td className="sub">
                    {formatTokens(row.input_tokens)} in / {formatTokens(row.output_tokens)} out
                  </td>
                </tr>
              ))}
              {report.by_agent_model.length === 0 && (
                <tr>
                  <td colSpan={3} className="sub">
                    Nothing yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
        <section aria-label="By project">
          <h4>By project</h4>
          <table className="usage-table">
            <tbody>
              {report.by_project.map((row) => (
                <tr key={row.key}>
                  <td>{row.project_name || "Unassigned"}</td>
                  <td>{formatCost(row.cost_usd)}</td>
                </tr>
              ))}
              {report.by_project.length === 0 && (
                <tr>
                  <td colSpan={2} className="sub">
                    Nothing yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      </div>
      <div className="usage-split-grid">
        <section aria-label="Top sessions by cost">
          <h4>Top sessions</h4>
          <ol className="usage-top-list">
            {report.top_sessions.map((s) => (
              <li key={s.id}>
                <span>{s.name}</span>
                <b>{formatCost(s.cost_usd)}</b>
              </li>
            ))}
            {report.top_sessions.length === 0 && <li className="sub">None yet.</li>}
          </ol>
        </section>
        <section aria-label="Top tasks by cost">
          <h4>Top tasks</h4>
          <ol className="usage-top-list">
            {report.top_tasks.map((t) => (
              <li key={t.id}>
                <span>{t.title}</span>
                <b>{formatCost(t.cost_usd)}</b>
              </li>
            ))}
            {report.top_tasks.length === 0 && <li className="sub">None yet.</li>}
          </ol>
        </section>
      </div>
    </div>
  );
}

function QuotaRow({ label, window }: { label: string; window: { used_percentage: number; resets_at: number } }) {
  return (
    <div className={`usage-quota-row ${quotaClass(window.used_percentage)}`}>
      <span>{label}</span>
      <i>
        <b style={{ width: `${Math.max(0, Math.min(100, window.used_percentage))}%` }} />
      </i>
      <span>
        {window.used_percentage}% {formatCountdown(window.resets_at)}
      </span>
    </div>
  );
}
