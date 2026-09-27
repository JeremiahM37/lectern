// Usage by provider (docs/budgets.md "Provider usage"): one card per agent
// CLI with its spend by day, the usage windows its logins last reported and
// a warning once one passes the warn line (80% by default, also pushed), a
// split by account where a CLI has several, and an estimated-cost table.
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import { formatCost, formatCountdown, formatTokens } from "../sessions/usageFormat";
import { t, useLocale } from "../i18n";

interface Win {
  agent: string;
  name: "5h" | "7d";
  used_percentage: number;
  resets_at: number;
  account: string;
  target: string;
  warn: boolean;
}
interface Day { date: string; cost_usd: number; input_tokens: number; output_tokens: number }
interface Account {
  key: string;
  label: string;
  target: string;
  cost_usd: number;
  estimated_usd: number;
  input_tokens: number;
  output_tokens: number;
  windows: Win[];
  warn: boolean;
}
export interface Provider {
  agent: string;
  label: string;
  cost_usd: number;
  estimated_usd: number;
  input_tokens: number;
  output_tokens: number;
  daily: Day[];
  windows: Win[];
  accounts: Account[];
  warn: boolean;
  peak_pct: number;
}
interface CostRow {
  agent: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  reported_usd: number;
  estimated_usd: number;
  list_usd?: number;
  priced: boolean;
}
export interface ProviderReport { days: number; warn_percent: number; providers: Provider[]; cost_table: CostRow[] }

const windowName = (name: "5h" | "7d") => t(`remote.usage.window.${name}`);

// The CLI's own login is "Default" from the server; show it translated.
const accountLabel = (label: string) => (label === "Default" ? t("remote.usage.defaultAccount") : label);

function Meter({ w, warnAt }: { w: Win; warnAt: number }) {
  const pct = Math.max(0, Math.min(100, w.used_percentage));
  return (
    <div className={`pu-meter${w.warn ? " warn" : ""}`} data-window={w.name}>
      <span className="pu-meter-label">
        {windowName(w.name)}
        {w.account !== "Default" || w.target ? <small> · {w.target ? t("remote.usage.accountOn", { account: accountLabel(w.account), machine: w.target }) : accountLabel(w.account)}</small> : null}
      </span>
      <i role="meter" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label={t("remote.usage.windowUsed", { window: windowName(w.name) })}>
        <b style={{ width: `${pct}%` }} />
        <em style={{ left: `${warnAt}%` }} aria-hidden />
      </i>
      <span className="pu-meter-value">
        {Math.round(w.used_percentage)}%{w.resets_at ? ` · ${formatCountdown(w.resets_at)}` : ""}
      </span>
    </div>
  );
}

function Bars({ days }: { days: Day[] }) {
  const max = Math.max(0.0001, ...days.map((d) => d.cost_usd));
  const tokensMax = Math.max(1, ...days.map((d) => d.input_tokens + d.output_tokens));
  const useTokens = days.every((d) => d.cost_usd === 0) && days.some((d) => d.input_tokens + d.output_tokens > 0);
  return (
    <div className="pu-bars" aria-label={useTokens ? t("remote.usage.tokensByDay") : t("remote.usage.costByDay")}>
      {days.map((d) => {
        const v = useTokens ? (d.input_tokens + d.output_tokens) / tokensMax : d.cost_usd / max;
        return (
          <span
            key={d.date}
            title={`${d.date}: ${formatCost(d.cost_usd)} · ${t("remote.usage.tokensInOut", { input: formatTokens(d.input_tokens), output: formatTokens(d.output_tokens) })}`}
          >
            <i style={{ height: `${v > 0 ? Math.max(4, v * 100) : 0}%` }} />
          </span>
        );
      })}
    </div>
  );
}

function ProviderCard({ p, warnAt }: { p: Provider; warnAt: number }) {
  const [accounts, setAccounts] = useState(false);
  const idle = p.cost_usd === 0 && p.input_tokens === 0 && p.windows.length === 0;
  return (
    <article className={`pu-card${p.warn ? " warn" : ""}${idle ? " idle" : ""}`} data-provider={p.agent}>
      <header>
        <h4>{p.label}</h4>
        {p.warn && <span className="chip warn" role="status">{t("remote.usage.over", { pct: warnAt })}</span>}
      </header>
      <div className="pu-figures">
        <b>{formatCost(p.cost_usd)}</b>
        <span className="sub">
          {t("remote.usage.tokensInOutDot", { input: formatTokens(p.input_tokens), output: formatTokens(p.output_tokens) })}
          {p.estimated_usd > 0 && ` · ${t("remote.usage.estimated", { cost: formatCost(p.estimated_usd) })}`}
        </span>
      </div>
      <Bars days={p.daily} />
      {p.windows.length > 0 ? (
        <div className="pu-windows">{p.windows.map((w, i) => <Meter key={i} w={w} warnAt={warnAt} />)}</div>
      ) : (
        <p className="sub pu-nowindow">{idle ? t("remote.usage.idle") : t("remote.usage.noWindows")}</p>
      )}
      {p.accounts.length > 1 && (
        <>
          <button type="button" className="linkish" aria-expanded={accounts} onClick={() => setAccounts(!accounts)}>
            {accounts ? t("remote.usage.hideAccounts", { n: p.accounts.length }) : t("remote.usage.byAccount", { n: p.accounts.length })}
          </button>
          {accounts && (
            <table className="usage-table pu-accounts">
              <tbody>
                {p.accounts.map((a) => (
                  <tr key={a.key} className={a.warn ? "warn" : ""}>
                    <td>{accountLabel(a.label)}<small className="sub"> {a.target}</small></td>
                    <td>{formatCost(a.cost_usd)}</td>
                    <td className="sub">
                      {a.windows.map((w) => `${windowName(w.name)} ${Math.round(w.used_percentage)}%`).join(" · ") || "—"}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </>
      )}
    </article>
  );
}

export function ProviderUsage({ api, days }: {
  api: { request<T>(path: string, o?: { method?: string; body?: JsonValue }): Promise<T> };
  days: number;
}) {
  useLocale();
  const [report, setReport] = useState<ProviderReport>();
  const [error, setError] = useState("");
  const [warn, setWarn] = useState("");
  useEffect(() => {
    let alive = true;
    api
      .request<ProviderReport>(`/usage/providers?days=${days}`)
      .then((r) => {
        if (!alive || !Array.isArray(r?.providers)) return;
        setReport(r);
        setWarn(String(r.warn_percent));
      })
      .catch((e) => alive && setError(String(e)));
    return () => {
      alive = false;
    };
  }, [api, days]);
  if (error) return <p className="usage-error">{error}</p>;
  if (!report) return null;
  const saveWarn = () => {
    const n = Number(warn);
    if (!(n >= 1 && n <= 100) || n === report.warn_percent) return;
    void api
      .request<Record<string, JsonValue>>("/budgets")
      .then((cfg) => api.request<ProviderReport>("/budgets", { method: "PUT", body: { ...cfg, usage_warn_percent: n } }))
      .then(() => api.request<ProviderReport>(`/usage/providers?days=${days}`))
      .then(setReport)
      .catch((e) => setError(String(e)));
  };
  const warned = report.providers.filter((p) => p.warn);
  return (
    <section className="provider-usage" aria-label={t("remote.usage.title")} data-setting="usage.providers">
      <div className="pu-head">
        <h4>{t("remote.usage.byProvider")}</h4>
        <label data-setting="usage.warnPercent">
          {t("remote.usage.warnAt")}
          <input
            type="number"
            min={1}
            max={100}
            value={warn}
            onChange={(e) => setWarn(e.target.value)}
            onBlur={saveWarn}
            onKeyDown={(e) => e.key === "Enter" && saveWarn()}
          />
          %
        </label>
      </div>
      {warned.length > 0 && (
        <p className="pu-alert" role="alert">
          {t("remote.usage.alert", { count: warned.length, names: warned.map((p) => p.label).join(", "), pct: report.warn_percent })}
        </p>
      )}
      <div className="pu-grid">
        {report.providers.map((p) => <ProviderCard key={p.agent} p={p} warnAt={report.warn_percent} />)}
      </div>
      <details className="pu-costs" open={report.cost_table.length > 0 && report.cost_table.length <= 6}>
        <summary>{t("remote.usage.costTable")}</summary>
        <table className="usage-table">
          <thead>
            <tr><th>{t("remote.usage.col.model")}</th><th>{t("remote.usage.col.tokens")}</th><th>{t("remote.usage.col.reported")}</th><th>{t("remote.usage.col.estimated")}</th><th>{t("remote.usage.col.list")}</th></tr>
          </thead>
          <tbody>
            {report.cost_table.map((c) => (
              <tr key={c.agent + c.model}>
                <td>{c.agent}{c.model ? ` · ${c.model}` : ""}</td>
                <td className="sub">{formatTokens(c.input_tokens)} / {formatTokens(c.output_tokens)}</td>
                <td>{formatCost(c.reported_usd)}</td>
                <td>{c.estimated_usd > 0 ? formatCost(c.estimated_usd) : "—"}</td>
                <td>{c.priced ? formatCost(c.list_usd || 0) : <span className="sub">{t("remote.usage.addPrice")}</span>}</td>
              </tr>
            ))}
            {report.cost_table.length === 0 && <tr><td colSpan={5} className="sub">{t("remote.usage.nothing")}</td></tr>}
          </tbody>
        </table>
        <p className="subhint">{t("remote.usage.listHint")}</p>
      </details>
    </section>
  );
}
