// The Usage view (Settings → "Usage & about"): totals by day for the last
// N days, split by agent/model and by project, today's/this week's spend,
// top sessions/tasks by cost, and the account-wide quota panel. Backed by
// GET /api/usage?days=N (internal/api/usage.go) — see its doc comment for
// what "combined session+task spend" means.
import { useEffect, useState } from "react";
import type { BudgetLimitStatus, BudgetPeriodStatus, UsageReport } from "../types";
import { formatAge, formatCost, formatCountdown, formatTokens, quotaClass } from "../sessions/usageFormat";
import type { JsonValue } from "../api";
import { ProviderUsage } from "../remote/ProviderUsage";
import { t, useLocale } from "../i18n";

export interface UsagePanelApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T>;
}

export function UsagePanel({ api }: { api: UsagePanelApi }) {
  useLocale();
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
  if (!report) return <p>{t("agentSettings.usage.loading")}</p>;
  const maxDay = Math.max(0.0001, ...report.daily.map((d) => d.cost_usd));
  const quota = report.quota;
  const budgets = report.budgets;
  const budgetRows: [string, BudgetLimitStatus][] = budgets
    ? [[t("agentSettings.usage.overall"), budgets.overall], ...Object.entries(budgets.per_agent)]
    : [];
  return (
    <div className="usage-panel">
      {budgetRows.some(([, l]) => l.daily || l.weekly) && (
        <section className="usage-budgets" aria-label={t("agentSettings.usage.budgets")}>
          <h4>{t("agentSettings.usage.budgets")}</h4>
          {budgetRows.map(([label, l]) =>
            (["daily", "weekly"] as const).map((period) => {
              const p = l[period];
              if (!p) return null;
              return <BudgetRow key={label + period} label={t("agentSettings.usage.budgetRowLabel", { label, period: period === "daily" ? t("agentSettings.usage.daily") : t("agentSettings.usage.weekly") })} period={p} />;
            }),
          )}
        </section>
      )}
      {!quota.empty && (
        <section className="usage-quota" aria-label={t("agentSettings.usage.accountQuota")}>
          <h4>{t("agentSettings.usage.accountQuota")}</h4>
          <div className={`usage-quota-rows${quota.stale ? " stale" : ""}`}>
            <QuotaRow label={t("agentSettings.usage.fiveHour")} window={quota.five_hour} />
            <QuotaRow label={t("agentSettings.usage.sevenDay")} window={quota.seven_day} />
          </div>
          {quota.stale && <p className="sub">{t("agentSettings.usage.quotaStale", { age: formatAge(quota.at) })}</p>}
        </section>
      )}
      <section className="usage-totals">
        <div className="usage-stat">
          <b>{formatCost(report.today_usd)}</b>
          <span>{t("agentSettings.usage.today")}</span>
        </div>
        <div className="usage-stat">
          <b>{formatCost(report.week_usd)}</b>
          <span>{t("agentSettings.usage.thisWeek")}</span>
        </div>
        <label className="usage-days">
          {t("agentSettings.usage.window")}{" "}
          <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
            {[7, 14, 30, 90].map((n) => (
              <option key={n} value={n}>
                {t("agentSettings.usage.days", { n })}
              </option>
            ))}
          </select>
        </label>
      </section>
      <ProviderUsage api={api} days={days} />
      <section className="usage-daily-chart" aria-label={t("agentSettings.usage.costByDay")}>
        <h4>{t("agentSettings.usage.costByDay")}</h4>
        <div className="usage-bars">
          {report.daily.map((d) => (
            <div className="usage-bar" key={d.date} title={`${d.date}: ${formatCost(d.cost_usd)}`}>
              <i style={{ height: `${Math.max(2, (d.cost_usd / maxDay) * 100)}%` }} />
            </div>
          ))}
          {report.daily.length === 0 && <p className="sub">{t("agentSettings.usage.noUsage")}</p>}
        </div>
      </section>
      <div className="usage-split-grid">
        <section aria-label={t("agentSettings.usage.byAgentModelLabel")}>
          <h4>{t("agentSettings.usage.byAgentModel")}</h4>
          <table className="usage-table">
            <tbody>
              {report.by_agent_model.map((row) => (
                <tr key={row.key}>
                  <td>
                    {row.agent}
                    {row.model ? ` · ${row.model}` : ""}
                  </td>
                  <td>
                    {formatCost(row.cost_usd)}
                    {(row.estimated_usd ?? 0) > 0 && (
                      <span className="sub" title={t("agentSettings.usage.estimatedTitle")}>
                        {" "}
                        {t("agentSettings.usage.estimated", { cost: formatCost(row.estimated_usd ?? 0) })}
                      </span>
                    )}
                  </td>
                  <td className="sub">
                    {t("agentSettings.usage.tokensInOut", { input: formatTokens(row.input_tokens), output: formatTokens(row.output_tokens) })}
                  </td>
                </tr>
              ))}
              {report.by_agent_model.length === 0 && (
                <tr>
                  <td colSpan={3} className="sub">
                    {t("agentSettings.usage.nothingYet")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
        <section aria-label={t("agentSettings.usage.byProject")}>
          <h4>{t("agentSettings.usage.byProject")}</h4>
          <table className="usage-table">
            <tbody>
              {report.by_project.map((row) => (
                <tr key={row.key}>
                  <td>{row.project_name || t("agentSettings.usage.unassigned")}</td>
                  <td>{formatCost(row.cost_usd)}</td>
                </tr>
              ))}
              {report.by_project.length === 0 && (
                <tr>
                  <td colSpan={2} className="sub">
                    {t("agentSettings.usage.nothingYet")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </section>
      </div>
      <div className="usage-split-grid">
        <section aria-label={t("agentSettings.usage.topSessionsLabel")}>
          <h4>{t("agentSettings.usage.topSessions")}</h4>
          <ol className="usage-top-list">
            {report.top_sessions.map((s) => (
              <li key={s.id}>
                <span>{s.name}</span>
                <b>{formatCost(s.cost_usd)}</b>
              </li>
            ))}
            {report.top_sessions.length === 0 && <li className="sub">{t("agentSettings.usage.noneYet")}</li>}
          </ol>
        </section>
        <section aria-label={t("agentSettings.usage.topTasksLabel")}>
          <h4>{t("agentSettings.usage.topTasks")}</h4>
          <ol className="usage-top-list">
            {report.top_tasks.map((task) => (
              <li key={task.id}>
                <span>{task.title}</span>
                <b>{formatCost(task.cost_usd)}</b>
              </li>
            ))}
            {report.top_tasks.length === 0 && <li className="sub">{t("agentSettings.usage.noneYet")}</li>}
          </ol>
        </section>
      </div>
    </div>
  );
}

// budgetClass mirrors quotaClass's thresholds, plus a hard "blocked" state
// for a stop-mode limit currently at or over 100%.
function budgetClass(p: BudgetPeriodStatus): string {
  if (p.blocked) return "budget-red budget-blocked";
  if (p.percent >= 90) return "budget-red";
  if (p.percent >= 75) return "budget-amber";
  return "";
}

function BudgetRow({ label, period }: { label: string; period: BudgetPeriodStatus }) {
  return (
    <div className={`usage-budget-row ${budgetClass(period)}`}>
      <span className="budget-label">{label}</span>
      <i>
        <b style={{ width: `${Math.max(0, Math.min(100, period.percent))}%` }} />
      </i>
      <span>
        {formatCost(period.spent_usd)} / {formatCost(period.cap_usd)}
        {period.blocked ? ` · ${t("agentSettings.usage.blocked")}` : ""}
      </span>
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
