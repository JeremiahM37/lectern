// A compact, always-visible chip showing Claude's account-wide 5h/7d quota
// (task: "the header (or a compact always-visible chip on board and
// sessions views) shows Claude 5h and 7d usage % ... turning amber/red as
// they fill; stale (>30 min old) data shown dimmed with its age"). It reuses
// GET /api/usage rather than a dedicated endpoint — the quota block is cheap
// to compute (one settings row), and `days=1` keeps the rest of the report
// small since this chip only reads the `quota` field.
import { useEffect, useState } from "react";
import { withToken } from "../api";
import type { BudgetStatus, UsageQuota } from "../types";
import { formatAge, formatCountdown, quotaClass } from "./usageFormat";

export interface QuotaChipApi {
  request<T>(path: string, options?: { method?: string }): Promise<T>;
}

export function QuotaChip({ api }: { api: QuotaChipApi }) {
  const [quota, setQuota] = useState<UsageQuota>();
  const [budgets, setBudgets] = useState<BudgetStatus>();
  useEffect(() => {
    let cancelled = false;
    const load = () =>
      api
        .request<{ quota: UsageQuota; budgets: BudgetStatus }>("/usage?days=1")
        .then((report) => {
          if (!cancelled) {
            setQuota(report.quota);
            setBudgets(report.budgets);
          }
        })
        .catch(() => {});
    void load();
    // The account-wide rate_limits setting only moves when some session's
    // statusline/rollout posts (internal/agentevents.IngestStatusline /
    // IngestCodexUsage), which already re-publishes that session's full row
    // on the same "board" bus channel App.tsx's own stream reads — listening
    // for it here is what makes this chip appear live rather than up to a
    // minute stale. The interval below is only the fallback for a dropped
    // connection.
    const stream = new EventSource(withToken("/api/stream"));
    stream.addEventListener("session", load);
    const timer = window.setInterval(load, 60_000);
    return () => {
      cancelled = true;
      stream.close();
      clearInterval(timer);
    };
  }, [api]);
  // Budgets (docs/budgets.md): a compact indicator beside the quota chip —
  // only rendered once at least one limit is actually configured, same
  // "nothing to show yet" convention the quota chip itself uses.
  const overallDaily = budgets?.overall.daily;
  const overallWeekly = budgets?.overall.weekly;
  const worst = [overallDaily, overallWeekly].filter((p): p is NonNullable<typeof p> => !!p)
    .sort((a, b) => b.percent - a.percent)[0];
  const budgetChip = worst && (
    <span
      className={`budget-chip${budgets?.any_blocked ? " budget-blocked" : ""}`}
      title={`Overall budget: $${worst.spent_usd.toFixed(2)} of $${worst.cap_usd.toFixed(2)}`}
    >
      ${worst.spent_usd.toFixed(0)}/{worst.cap_usd.toFixed(0)}
      {budgets?.any_blocked ? " · blocked" : ""}
    </span>
  );

  if (!quota || quota.empty) return budgetChip ?? null;
  const stale = quota.stale;
  return (
    <>
      <div className={`quota-chip${stale ? " stale" : ""}`} title={stale ? `Last updated ${formatAge(quota.at)}` : undefined}>
        <span className={`quota-window ${quotaClass(quota.five_hour.used_percentage)}`}>
          5h {quota.five_hour.used_percentage}%
          {quota.five_hour.resets_at ? <small> {formatCountdown(quota.five_hour.resets_at)}</small> : null}
        </span>
        <span className={`quota-window ${quotaClass(quota.seven_day.used_percentage)}`}>
          7d {quota.seven_day.used_percentage}%
          {quota.seven_day.resets_at ? <small> {formatCountdown(quota.seven_day.resets_at)}</small> : null}
        </span>
        {stale && <span className="quota-stale-note">{formatAge(quota.at)}</span>}
      </div>
      {budgetChip}
    </>
  );
}
