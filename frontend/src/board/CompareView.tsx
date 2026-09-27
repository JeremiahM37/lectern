import type { AttemptSummary } from "../types";
import { t, useLocale } from "../i18n";
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
    return rc === 0 ? t("board.compare.checkPass") : t("board.compare.checkFail", { rc: String(rc) });
  }
  return t("board.compare.noCheck");
}

// costPerPass is this one attempt's own $/pass (docs/outcomes.md): its cost
// divided by 1 when its check passed, "—" when the check failed/never ran or
// the cost is unknown — never a divide-by-zero artifact.
function costPerPass(a: AttemptSummary): string {
  const passed = a.verify && Object.keys(a.verify).length > 0 && a.verify.rc === 0;
  if (!passed || a.cost_usd == null) return "—";
  return `$${Number(a.cost_usd).toFixed(3)}`;
}

function diffSummary(a: AttemptSummary): string {
  if (!a.diff_stat.length) return t("board.compare.noDiff");
  let add = 0,
    del = 0;
  for (const f of a.diff_stat) {
    add += f.additions ?? 0;
    del += f.deletions ?? 0;
  }
  return t("board.compare.diffSummary", { files: a.diff_stat.length, add, del });
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
  useLocale();
  const allDone = attempts.every((a) =>
    ["done", "failed", "cancelled"].includes(a.status),
  );
  return (
    <div className="compare-view">
      <div className="compare-head">
        <span className="subhint">
          {t("board.compare.intro", { n: attempts.length })}
        </span>
        <button
          className="b"
          disabled={!allDone || judging}
          title={!allDone ? t("board.compare.waitAll") : undefined}
          onClick={onJudge}
        >
          {judging ? t("board.compare.judging") : t("board.compare.judge")}
        </button>
      </div>
      {judgment && (
        <div className="judge-verdict">
          {t("board.compare.judgePicked")}<b>{t("board.compare.attemptN", { n: judgment.winner_attempt })}</b>{t("board.compare.judgeDash")}{" "}
          {judgment.reason}
        </div>
      )}
      <div className="compare-grid">
        {attempts.map((a) => (
          <article className="compare-card" key={a.n}>
            <header>
              <b>⑂ #{a.n}</b>{" "}
              <span className={`statpill s-${a.status}`}>{t(`board.status.${a.status}`, undefined, a.status)}</span>
            </header>
            <dl>
              <dt>{t("board.compare.agent")}</dt>
              <dd>
                {a.agent}
                {a.model && ` · ${a.model}`}
              </dd>
              <dt>{t("board.compare.permission")}</dt>
              <dd>{a.permission_mode || "—"}</dd>
              <dt>{t("board.compare.check")}</dt>
              <dd>{checkBadge(a)}</dd>
              <dt>{t("board.compare.duration")}</dt>
              <dd>{duration(a)}</dd>
              <dt>{t("board.compare.costTokens")}</dt>
              <dd>
                {a.cost_usd != null ? `$${Number(a.cost_usd).toFixed(3)}` : "—"}
                {(a.input_tokens != null || a.output_tokens != null) &&
                  t("board.compare.tokensInOut", { input: String(a.input_tokens ?? 0), output: String(a.output_tokens ?? 0) })}
              </dd>
              <dt>{t("board.compare.costPerPass")}</dt>
              <dd>{costPerPass(a)}</dd>
              <dt>{t("board.compare.diff")}</dt>
              <dd>{diffSummary(a)}</dd>
            </dl>
            <div className="btnrow">
              <button className="b" onClick={() => onViewDiff(a.n)}>
                {t("board.compare.viewDiff")}
              </button>
              <button
                className="b ok"
                disabled={busy || a.status !== "done"}
                onClick={() => onPick(a.n)}
              >
                {t("board.compare.pick")}
              </button>
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}
