// The Outcomes view (Settings → "Usage & about", right below Usage): cost
// per outcome — $ per passing task, $ per accepted change, $ per 100 kept
// lines, passes per $10, median time to a passing check — grouped by agent,
// model or project, over a configurable window. Backed by
// GET /api/outcomes?days=N&group=agent|model|project (internal/api/outcomes.go).
// See docs/outcomes.md for what each number means and its caveats.
import { useEffect, useMemo, useState } from "react";
import type { OutcomeRow, OutcomesReport } from "../types";
import { formatCost } from "../sessions/usageFormat";

export interface OutcomesPanelApi {
  request<T>(path: string): Promise<T>;
}

type Group = "agent" | "model" | "project";
type SortKey = keyof Pick<
  OutcomeRow,
  "cost_usd" | "cost_per_pass" | "cost_per_accepted" | "cost_per_100_lines" | "passes_per_10usd" | "median_time_to_pass_s"
>;

const GROUPS: { key: Group; label: string }[] = [
  { key: "agent", label: "Agent" },
  { key: "model", label: "Model" },
  { key: "project", label: "Project" },
];

const COLUMNS: { key: SortKey; label: string; fmt(v?: number): string }[] = [
  { key: "cost_usd", label: "Total cost", fmt: (v) => formatCost(v ?? 0) },
  { key: "cost_per_pass", label: "$/pass", fmt: fmtCost },
  { key: "cost_per_accepted", label: "$/accepted", fmt: fmtCost },
  { key: "cost_per_100_lines", label: "$/100 lines", fmt: fmtCost },
  { key: "passes_per_10usd", label: "Passes/$10", fmt: (v) => (v == null ? "—" : v.toFixed(1)) },
  { key: "median_time_to_pass_s", label: "Median time to pass", fmt: fmtDuration },
];

function fmtCost(v?: number): string {
  return v == null ? "—" : formatCost(v);
}
function fmtDuration(v?: number): string {
  if (v == null) return "—";
  return v < 90 ? `${v.toFixed(0)}s` : `${(v / 60).toFixed(1)}m`;
}

export function OutcomesPanel({ api }: { api: OutcomesPanelApi }) {
  const [days, setDays] = useState(30);
  const [group, setGroup] = useState<Group>("agent");
  const [report, setReport] = useState<OutcomesReport>();
  const [error, setError] = useState("");
  const [sortKey, setSortKey] = useState<SortKey>("cost_usd");
  const [sortDesc, setSortDesc] = useState(true);

  useEffect(() => {
    let cancelled = false;
    api
      .request<OutcomesReport>(`/outcomes?days=${days}&group=${group}`)
      .then((r) => {
        if (!cancelled) setReport(r);
      })
      .catch((e) => !cancelled && setError(String(e)));
    return () => {
      cancelled = true;
    };
  }, [api, days, group]);

  const rows = useMemo(() => {
    if (!report) return [];
    const list = [...report.rows];
    list.sort((a, b) => {
      const av = a[sortKey] ?? -Infinity;
      const bv = b[sortKey] ?? -Infinity;
      return sortDesc ? bv - av : av - bv;
    });
    return list;
  }, [report, sortKey, sortDesc]);

  function toggleSort(key: SortKey) {
    if (key === sortKey) {
      setSortDesc((d) => !d);
    } else {
      setSortKey(key);
      setSortDesc(true);
    }
  }

  if (error) return <p className="usage-error">{error}</p>;
  if (!report) return <p>Loading outcomes…</p>;

  const maxCost = Math.max(0.0001, ...rows.map((r) => r.cost_usd));

  return (
    <div className="outcomes-panel">
      <section className="usage-totals">
        <label className="usage-days">
          Group by{" "}
          <select value={group} onChange={(e) => setGroup(e.target.value as Group)}>
            {GROUPS.map((g) => (
              <option key={g.key} value={g.key}>
                {g.label}
              </option>
            ))}
          </select>
        </label>
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

      <section aria-label="Spend comparison">
        <h4>Spend by {group}</h4>
        <ol className="usage-top-list outcomes-bars">
          {rows.map((r) => (
            <li key={r.key} className="outcomes-bar-row">
              <span>{r.label}</span>
              <i className="outcomes-bar-track">
                <b style={{ width: `${Math.max(2, (r.cost_usd / maxCost) * 100)}%` }} />
              </i>
              <b>{formatCost(r.cost_usd)}</b>
            </li>
          ))}
          {rows.length === 0 && <li className="sub">Nothing yet — run a task or a check to see outcomes here.</li>}
        </ol>
      </section>

      <section aria-label="Outcomes table">
        <h4>Cost per outcome</h4>
        <div style={{ overflowX: "auto" }}>
          <table className="usage-table outcomes-table">
            <thead>
              <tr>
                <th>{group === "agent" ? "Agent" : group === "model" ? "Model" : "Project"}</th>
                <th>Passed</th>
                <th>Accepted</th>
                {COLUMNS.map((c) => (
                  <th key={c.key}>
                    <button
                      type="button"
                      className="outcomes-sort-btn"
                      aria-sort={sortKey === c.key ? (sortDesc ? "descending" : "ascending") : "none"}
                      onClick={() => toggleSort(c.key)}
                    >
                      {c.label}
                      {sortKey === c.key ? (sortDesc ? " ▼" : " ▲") : ""}
                    </button>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.key}>
                  <td>
                    {r.label}
                    {r.partial && (
                      <span className="outcomes-flag" title="Some of this row's cost has no measured or estimated source">
                        {" "}
                        partial
                      </span>
                    )}
                    {r.estimated && !r.partial && (
                      <span className="outcomes-flag" title="Some of this row's cost is a token-based estimate, not a measured figure">
                        {" "}
                        estimated
                      </span>
                    )}
                  </td>
                  <td>
                    {r.checked ? `${r.passed}/${r.checked}` : "—"}
                  </td>
                  <td>{r.accepted || "—"}</td>
                  <td>{formatCost(r.cost_usd)}</td>
                  <td>{fmtCost(r.cost_per_pass)}</td>
                  <td>{fmtCost(r.cost_per_accepted)}</td>
                  <td>{fmtCost(r.cost_per_100_lines)}</td>
                  <td>{r.passes_per_10usd == null ? "—" : r.passes_per_10usd.toFixed(1)}</td>
                  <td>{fmtDuration(r.median_time_to_pass_s)}</td>
                </tr>
              ))}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={9} className="sub">
                    Nothing yet.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <p className="sub">
          Costs prefer exact OpenTelemetry figures, then the agent's own reported cost, then a configured per-model
          token estimate — rows marked "partial" or "estimated" are not fully measured. See docs/outcomes.md.
        </p>
      </section>
    </div>
  );
}
