import { useEffect, useMemo, useState } from "react";
import type { Project } from "../types";
import type { JsonValue } from "../api";
import { Modal } from "../sessions/Modal";
import { DiffViewer } from "../review/DiffViewer";
import "./evals.css";

export interface EvalsApi {
  request<T>(
    path: string,
    options?: { method?: string; body?: JsonValue },
  ): Promise<T>;
}

interface Suite {
  id: number;
  name: string;
  project_id: number;
  description: string;
  created_at: number;
}
interface Case {
  id: number;
  suite_id: number;
  name: string;
  prompt: string;
  base_ref: string;
  check_command: string;
  timeout_s: number;
  setup_command: string;
}
interface Run {
  id: number;
  suite_id: number;
  created_at: number;
  status: string;
  repeats: number;
  notes: string;
}
interface Variant {
  agent: string;
  model: string;
  permission_mode: string;
}
interface Result {
  id: number;
  case_id: number;
  variant_idx: number;
  repeat_idx: number;
  task_id: number | null;
  attempt_id: number | null;
  status: string;
  duration_s: number | null;
  cost_usd: number | null;
  check_rc: number | null;
  check_output_tail: string;
  diff_files: number | null;
  diff_lines: number | null;
}
interface VariantStats {
  variant_idx: number;
  total: number;
  passed: number;
  failed: number;
  errored: number;
  pass_rate: number;
  mean_duration_s: number;
  total_cost_usd: number;
  mean_input_tokens: number;
  mean_output_tokens: number;
  cost_per_pass?: number;
}
interface RunView {
  run: Run;
  suite: Suite;
  cases: Case[];
  variants: Variant[];
  results: Result[];
  leaderboard: VariantStats[];
}
interface CaseComparison {
  case_id: number;
  pass_rate_a: number;
  pass_rate_b: number;
  delta: number;
  status: string;
}

type NewVariant = { agent: string; model: string; permissionMode: string };

function statusColor(status: string): string {
  switch (status) {
    case "passed":
      return "ev-pass";
    case "failed":
      return "ev-fail";
    case "error":
    case "timeout":
      return "ev-error";
    case "running":
      return "ev-running";
    default:
      return "ev-queued";
  }
}

export function Evals({
  api,
  projects,
  onClose,
  onNotice,
}: {
  api: EvalsApi;
  projects: Project[];
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  const [projectId, setProjectId] = useState(projects[0]?.id ?? 0);
  const [suites, setSuites] = useState<Suite[]>([]);
  const [suiteID, setSuiteID] = useState<number>();
  const [cases, setCases] = useState<Case[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [runID, setRunID] = useState<number>();
  const [runView, setRunView] = useState<RunView>();
  const [busy, setBusy] = useState(false);
  const [newSuiteName, setNewSuiteName] = useState("");
  const [newSuiteDesc, setNewSuiteDesc] = useState("");
  const [newCase, setNewCase] = useState({
    name: "",
    prompt: "",
    base_ref: "",
    check_command: "",
    timeout_s: 900,
    setup_command: "",
  });
  const [variants, setVariants] = useState<NewVariant[]>([
    { agent: "claude", model: "", permissionMode: "" },
  ]);
  const [repeats, setRepeats] = useState(1);
  const [compareWith, setCompareWith] = useState<Set<number>>(new Set());
  const [comparison, setComparison] = useState<{
    a: number;
    b: number;
    cases: CaseComparison[];
  }>();
  const [openCell, setOpenCell] = useState<Result>();
  const [cellDiff, setCellDiff] = useState<{
    stats: { path: string; additions?: number; deletions?: number }[];
    files: { path: string; patch: string }[];
  }>();

  const suite = suites.find((s) => s.id === suiteID);

  async function loadSuites(pid: number) {
    try {
      setSuites(await api.request<Suite[]>(`/evals/suites?project_id=${pid}`));
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  useEffect(() => {
    if (projectId) void loadSuites(projectId);
  }, [projectId]);

  async function openSuite(id: number) {
    setSuiteID(id);
    setRunID(undefined);
    setRunView(undefined);
    try {
      const detail = await api.request<{ suite: Suite; cases: Case[] }>(`/evals/suites/${id}`);
      setCases(detail.cases);
      setRuns(await api.request<Run[]>(`/evals/suites/${id}/runs`));
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function createSuite() {
    if (!newSuiteName.trim()) return onNotice("Name required", true);
    try {
      const s = await api.request<Suite>("/evals/suites", {
        method: "POST",
        body: { name: newSuiteName.trim(), project_id: projectId, description: newSuiteDesc },
      });
      setNewSuiteName("");
      setNewSuiteDesc("");
      await loadSuites(projectId);
      await openSuite(s.id);
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function importFromRepo() {
    setBusy(true);
    try {
      const result = await api.request<{ imported: { name: string }[]; failed: { path: string; error: string }[] }>(
        "/evals/suites/import",
        { method: "POST", body: { project_id: projectId } },
      );
      await loadSuites(projectId);
      onNotice(
        `Imported ${result.imported.length} suite(s)` +
          (result.failed.length ? `, ${result.failed.length} failed` : ""),
        result.failed.length > 0 && result.imported.length === 0,
      );
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }

  async function addCase() {
    if (!suiteID) return;
    if (!newCase.name.trim() || !newCase.prompt.trim())
      return onNotice("Name and prompt are required", true);
    try {
      await api.request(`/evals/suites/${suiteID}/cases`, { method: "POST", body: newCase });
      setNewCase({ name: "", prompt: "", base_ref: "", check_command: "", timeout_s: 900, setup_command: "" });
      const detail = await api.request<{ suite: Suite; cases: Case[] }>(`/evals/suites/${suiteID}`);
      setCases(detail.cases);
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function deleteCase(id: number) {
    if (!suiteID) return;
    await api.request(`/evals/cases/${id}`, { method: "DELETE" });
    const detail = await api.request<{ suite: Suite; cases: Case[] }>(`/evals/suites/${suiteID}`);
    setCases(detail.cases);
  }

  async function startRun() {
    if (!suiteID) return;
    setBusy(true);
    try {
      const run = await api.request<Run>(`/evals/suites/${suiteID}/runs`, {
        method: "POST",
        body: {
          variants: variants.map((v) => ({
            agent: v.agent,
            model: v.model,
            permission_mode: v.permissionMode,
          })),
          repeats,
        },
      });
      setRuns(await api.request<Run[]>(`/evals/suites/${suiteID}/runs`));
      await openRun(run.id);
      onNotice("Run started");
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }

  async function openRun(id: number) {
    setRunID(id);
    setComparison(undefined);
    setOpenCell(undefined);
    setCellDiff(undefined);
    try {
      setRunView(await api.request<RunView>(`/evals/runs/${id}`));
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  // Poll a live run so the matrix fills in without a manual refresh.
  useEffect(() => {
    if (!runID || runView?.run.status === "done" || runView?.run.status === "cancelled") return;
    const t = setInterval(() => void openRun(runID), 2000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runID, runView?.run.status]);

  async function cancelRun() {
    if (!runID) return;
    try {
      await api.request(`/evals/runs/${runID}/cancel`, { method: "POST" });
      await openRun(runID);
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function runCompare() {
    const ids = Array.from(compareWith);
    const [a, b] = ids;
    if (ids.length !== 2 || a == null || b == null)
      return onNotice("Pick exactly two runs to compare", true);
    try {
      const result = await api.request<{ cases: CaseComparison[] }>(
        `/evals/runs/${a}/compare/${b}`,
      );
      setComparison({ a, b, cases: result.cases });
    } catch (e) {
      onNotice(String(e), true);
    }
  }

  async function openCellDetail(r: Result) {
    setOpenCell(r);
    setCellDiff(undefined);
    if (!r.task_id) return;
    try {
      setCellDiff(await api.request(`/tasks/${r.task_id}/diff`));
    } catch {
      // no diff yet (e.g. the cell errored before any attempt ran) — the
      // panel still shows status/check output without it
    }
  }

  const matrix = useMemo(() => {
    if (!runView) return [];
    const byCell = new Map<string, Result[]>();
    for (const r of runView.results) {
      const key = `${r.case_id}:${r.variant_idx}`;
      const list = byCell.get(key) ?? [];
      list.push(r);
      byCell.set(key, list);
    }
    return runView.cases.map((c) => ({
      case: c,
      cells: runView.variants.map((v, vi) => ({
        variant: v,
        results: byCell.get(`${c.id}:${vi}`) ?? [],
      })),
    }));
  }, [runView]);

  return (
    <Modal id="evals-sheet" open className="sheet evals-sheet" aria-label="Agent tests" onCancel={onClose}>
      <header className="sheet-head">
        <h2>Agent tests</h2>
        <button onClick={onClose}>✕</button>
      </header>

      {!suite && (
        <>
          <label>
            Project
            <select id="ev-project" value={projectId} onChange={(e) => setProjectId(Number(e.target.value))}>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <div className="btnrow">
            <button id="ev-import-repo" className="b" disabled={busy} onClick={() => void importFromRepo()}>
              Import from repo (.lectern/evals/*.yaml)
            </button>
          </div>
          <div className="ev-suite-list">
            {suites.map((s) => (
              <article key={s.id} className="ev-suite-row" onClick={() => void openSuite(s.id)}>
                <b>{s.name}</b>
                <span className="subhint">{s.description}</span>
              </article>
            ))}
            {suites.length === 0 && <p className="subhint">No suites yet for this project.</p>}
          </div>
          <div className="ev-new-suite">
            <label>
              New suite name
              <input id="ev-new-suite-name" value={newSuiteName} onChange={(e) => setNewSuiteName(e.target.value)} />
            </label>
            <label>
              Description
              <input value={newSuiteDesc} onChange={(e) => setNewSuiteDesc(e.target.value)} />
            </label>
            <button id="ev-create-suite" className="b ok" onClick={() => void createSuite()}>
              + Create suite
            </button>
          </div>
        </>
      )}

      {suite && !runID && (
        <>
          <div className="btnrow">
            <button
              className="b"
              onClick={() => {
                setSuiteID(undefined);
                setCompareWith(new Set());
              }}
            >
              ← Suites
            </button>
            <b>{suite.name}</b>
          </div>

          <h3>Cases</h3>
          <div className="ev-case-list">
            {cases.map((c) => (
              <article key={c.id} className="ev-case-row">
                <div>
                  <b>{c.name}</b>
                  <span className="subhint">
                    {c.check_command || "no check command (falls back to project verify)"}
                  </span>
                </div>
                <button className="b no" onClick={() => void deleteCase(c.id)}>
                  ✕
                </button>
              </article>
            ))}
            {cases.length === 0 && <p className="subhint">No cases yet.</p>}
          </div>
          <div className="ev-new-case">
            <input
              placeholder="case name"
              id="ev-case-name"
              value={newCase.name}
              onChange={(e) => setNewCase({ ...newCase, name: e.target.value })}
            />
            <textarea
              placeholder="prompt"
              id="ev-case-prompt"
              value={newCase.prompt}
              onChange={(e) => setNewCase({ ...newCase, prompt: e.target.value })}
            />
            <input
              placeholder="base_ref (default branch if empty)"
              value={newCase.base_ref}
              onChange={(e) => setNewCase({ ...newCase, base_ref: e.target.value })}
            />
            <input
              placeholder="check_command (falls back to project verify)"
              value={newCase.check_command}
              onChange={(e) => setNewCase({ ...newCase, check_command: e.target.value })}
            />
            <input
              placeholder="setup_command (run first, optional)"
              value={newCase.setup_command}
              onChange={(e) => setNewCase({ ...newCase, setup_command: e.target.value })}
            />
            <input
              type="number"
              min={1}
              value={newCase.timeout_s}
              onChange={(e) => setNewCase({ ...newCase, timeout_s: Number(e.target.value) })}
            />
            <button id="ev-add-case" className="b ok" onClick={() => void addCase()}>
              + Add case
            </button>
          </div>

          <h3>Runs</h3>
          <div className="ev-run-list">
            {runs.map((r) => (
              <article key={r.id} className="ev-run-row">
                <input
                  type="checkbox"
                  aria-label={`Select run ${r.id} to compare`}
                  checked={compareWith.has(r.id)}
                  onChange={(e) => {
                    setCompareWith((prev) => {
                      const next = new Set(prev);
                      if (e.target.checked) next.add(r.id);
                      else next.delete(r.id);
                      return next;
                    });
                  }}
                />
                <span className={`ev-badge ${statusColor(r.status)}`}>{r.status}</span>
                <span>
                  run #{r.id} · {r.repeats} repeat(s)
                </span>
                <button className="b" onClick={() => void openRun(r.id)}>
                  Open
                </button>
              </article>
            ))}
            {runs.length === 0 && <p className="subhint">No runs yet.</p>}
          </div>
          {compareWith.size === 2 && (
            <button className="b ok" onClick={() => void runCompare()}>
              Compare selected runs
            </button>
          )}
          {comparison && (
            <div className="ev-compare">
              <h4>
                Run #{comparison.a} → #{comparison.b}
              </h4>
              <table>
                <thead>
                  <tr>
                    <th>Case</th>
                    <th>A</th>
                    <th>B</th>
                    <th>Δ</th>
                  </tr>
                </thead>
                <tbody>
                  {comparison.cases.map((c) => (
                    <tr key={c.case_id} className={`ev-cmp-${c.status}`}>
                      <td>{cases.find((cs) => cs.id === c.case_id)?.name ?? `case ${c.case_id}`}</td>
                      <td>{Math.round(c.pass_rate_a * 100)}%</td>
                      <td>{Math.round(c.pass_rate_b * 100)}%</td>
                      <td>
                        {c.delta > 0 ? "▲" : c.delta < 0 ? "▼" : "="} {c.status}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <h3>New run</h3>
          <div className="ev-variants">
            {variants.map((v, i) => (
              <div className="variant-row" key={i}>
                <select
                  value={v.agent}
                  onChange={(e) => {
                    const val = e.target.value;
                    setVariants((list) => list.map((x, j) => (j === i ? { ...x, agent: val } : x)));
                  }}
                >
                  <option value="claude">claude</option>
                  <option value="codex">codex</option>
                  <option value="gemini">gemini</option>
                </select>
                <input
                  placeholder="model"
                  value={v.model}
                  onChange={(e) => {
                    const val = e.target.value;
                    setVariants((list) => list.map((x, j) => (j === i ? { ...x, model: val } : x)));
                  }}
                />
                <select
                  value={v.permissionMode}
                  onChange={(e) => {
                    const val = e.target.value;
                    setVariants((list) =>
                      list.map((x, j) => (j === i ? { ...x, permissionMode: val } : x)),
                    );
                  }}
                >
                  <option value="">acceptEdits</option>
                  <option value="plan">plan</option>
                  <option value="bypassPermissions">bypass</option>
                </select>
                {variants.length > 1 && (
                  <button
                    className="variant-remove"
                    onClick={() => setVariants((list) => list.filter((_, j) => j !== i))}
                  >
                    ✕
                  </button>
                )}
              </div>
            ))}
            <button
              className="b"
              disabled={variants.length >= 8}
              onClick={() =>
                setVariants((v) => [...v, { agent: "claude", model: "", permissionMode: "" }])
              }
            >
              + Add variant
            </button>
          </div>
          <label>
            Repeats
            <input
              type="number"
              min={1}
              max={10}
              value={repeats}
              onChange={(e) => setRepeats(Number(e.target.value))}
            />
          </label>
          <button id="ev-run-suite" className="b ok" disabled={busy || cases.length === 0} onClick={() => void startRun()}>
            ▶ Run suite
          </button>
        </>
      )}

      {suite && runID && runView && (
        <>
          <div className="btnrow">
            <button className="b" onClick={() => setRunID(undefined)}>
              ← {suite.name}
            </button>
            <span className={`ev-badge ${statusColor(runView.run.status)}`}>{runView.run.status}</span>
            {["queued", "running"].includes(runView.run.status) && (
              <button className="b no" onClick={() => void cancelRun()}>
                ■ Cancel
              </button>
            )}
          </div>

          <h3>Matrix</h3>
          <div className="ev-matrix-wrap">
            <table className="ev-matrix">
              <thead>
                <tr>
                  <th>Case</th>
                  {runView.variants.map((v, i) => (
                    <th key={i}>
                      v{i + 1}: {v.agent}
                      {v.model && ` · ${v.model}`}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {matrix.map(({ case: c, cells }) => (
                  <tr key={c.id}>
                    <td>{c.name}</td>
                    {cells.map((cell, i) => {
                      const passed = cell.results.filter((r) => r.status === "passed").length;
                      const total = cell.results.length;
                      const worst = cell.results.find((r) => r.status !== "passed") ?? cell.results[0];
                      return (
                        <td
                          key={i}
                          className={`ev-cell ${worst ? statusColor(worst.status) : ""}`}
                          onClick={() => worst && void openCellDetail(worst)}
                        >
                          {total ? `${passed}/${total}` : "—"}
                        </td>
                      );
                    })}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {openCell && (
            <div className="ev-cell-detail">
              <header>
                <b className={`ev-badge ${statusColor(openCell.status)}`}>{openCell.status}</b>
                {openCell.duration_s != null && ` · ${openCell.duration_s.toFixed(1)}s`}
                {openCell.cost_usd != null && ` · $${openCell.cost_usd.toFixed(3)}`}
                {openCell.check_rc != null && ` · check rc=${openCell.check_rc}`}
                <button className="b" onClick={() => setOpenCell(undefined)}>
                  ✕
                </button>
              </header>
              {openCell.check_output_tail && <pre className="ev-check-output">{openCell.check_output_tail}</pre>}
              {cellDiff && <DiffViewer files={cellDiff.files} stats={cellDiff.stats} wrap={false} />}
            </div>
          )}

          <h3>Leaderboard</h3>
          <table className="ev-leaderboard">
            <thead>
              <tr>
                <th>Variant</th>
                <th>Pass rate</th>
                <th>Mean duration</th>
                <th>Total cost</th>
                <th>$/pass</th>
                <th>Mean tokens</th>
              </tr>
            </thead>
            <tbody>
              {runView.leaderboard.map((row) => {
                const v = runView.variants[row.variant_idx];
                return (
                  <tr key={row.variant_idx}>
                    <td>
                      v{row.variant_idx + 1}
                      {v && `: ${v.agent}${v.model ? ` · ${v.model}` : ""}`}
                    </td>
                    <td>
                      {Math.round(row.pass_rate * 100)}% ({row.passed}/{row.passed + row.failed + row.errored})
                    </td>
                    <td>{row.mean_duration_s ? `${row.mean_duration_s.toFixed(1)}s` : "—"}</td>
                    <td>${row.total_cost_usd.toFixed(3)}</td>
                    <td>{row.cost_per_pass ? `$${row.cost_per_pass.toFixed(3)}` : "—"}</td>
                    <td>
                      {row.mean_input_tokens ? Math.round(row.mean_input_tokens) : 0}in/
                      {row.mean_output_tokens ? Math.round(row.mean_output_tokens) : 0}out
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </>
      )}
    </Modal>
  );
}
