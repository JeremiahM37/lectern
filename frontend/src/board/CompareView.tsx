import type { AttemptSummary } from "../types";
import "./board.css";

export interface JudgeVerdict {
  winner_attempt: number;
  reason: string;
  judge_task_id: number;
}

function duration(a: AttemptSummary): string {
  if (!a.started_at || !a.finished_at) return "—";
  const s = a.finished_at - a.started_at;
  return s < 90 ? `${s.toFixed(0)}s` : `${(s / 60).toFixed(1)}m`;
}

function checkBadge(a: AttemptSummary): string {
  if (a.verify && Object.keys(a.verify).length > 0) {
    const rc = a.verify.rc;
    return rc === 0 ? "check ✓ pass" : `check ✗ fail (rc ${String(rc)})`;
  }
  return "no check run";
}

function diffSummary(a: AttemptSummary): string {
  if (!a.diff_stat.length) return "no diff yet";
  let add = 0,
    del = 0;
  for (const f of a.diff_stat) {
    add += f.additions ?? 0;
    del += f.deletions ?? 0;
  }
  return `${a.diff_stat.length} file(s) · +${add}/-${del}`;
}

// CompareView is the Best-of-N side-by-side: one card per attempt with
// everything the operator needs to pick a winner without opening each one.
export function CompareView({
  attempts,
  judgment,
  busy,
  judging,
  onViewDiff,
  onPick,
  onJudge,
}: {
  attempts: AttemptSummary[];
  judgment?: JudgeVerdict;
  busy: boolean;
  judging: boolean;
  onViewDiff(n: number): void;
  onPick(n: number): void;
  onJudge(): void;
}) {
  const allDone = attempts.every((a) =>
    ["done", "failed", "cancelled"].includes(a.status),
  );
  return (
    <div className="compare-view">
      <div className="compare-head">
        <span className="subhint">
          {attempts.length} attempts, side by side.
        </span>
        <button
          className="b"
          disabled={!allDone || judging}
          title={!allDone ? "Wait for every attempt to finish first" : undefined}
          onClick={onJudge}
        >
          {judging ? "Dispatching judge…" : "⚖ Judge"}
        </button>
      </div>
      {judgment && (
        <div className="judge-verdict">
          Judge picked <b>attempt #{judgment.winner_attempt}</b> —{" "}
          {judgment.reason}
        </div>
      )}
      <div className="compare-grid">
        {attempts.map((a) => (
          <article className="compare-card" key={a.n}>
            <header>
              <b>⑂ #{a.n}</b>{" "}
              <span className={`statpill s-${a.status}`}>{a.status}</span>
            </header>
            <dl>
              <dt>Agent</dt>
              <dd>
                {a.agent}
                {a.model && ` · ${a.model}`}
              </dd>
              <dt>Permission</dt>
              <dd>{a.permission_mode || "—"}</dd>
              <dt>Check</dt>
              <dd>{checkBadge(a)}</dd>
              <dt>Duration</dt>
              <dd>{duration(a)}</dd>
              <dt>Cost / tokens</dt>
              <dd>
                {a.cost_usd != null ? `$${Number(a.cost_usd).toFixed(3)}` : "—"}
                {(a.input_tokens != null || a.output_tokens != null) &&
                  ` · ${String(a.input_tokens ?? 0)}in/${String(a.output_tokens ?? 0)}out`}
              </dd>
              <dt>Diff</dt>
              <dd>{diffSummary(a)}</dd>
            </dl>
            <div className="btnrow">
              <button className="b" onClick={() => onViewDiff(a.n)}>
                ± View diff
              </button>
              <button
                className="b ok"
                disabled={busy || a.status !== "done"}
                onClick={() => onPick(a.n)}
              >
                ✓ Pick this one
              </button>
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}
