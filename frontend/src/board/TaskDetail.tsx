import { OpenInEditor } from "../remote/OpenInEditor";
import { LimitBanner } from "../limits/LimitBanner";
import { useEffect, useState } from "react";
import type { Event, TaskView } from "../types";
import { withToken, type JsonValue } from "../api";
import { Modal } from "../sessions/Modal";
import { DiffViewer } from "../review/DiffViewer";
import { CommentTray } from "../review/CommentTray";
import { DiffModeToggle } from "../review/DiffModeToggle";
import { PREF_KEYS, useStoredFlag, useStoredPref } from "../review/prefs";
import type { DiffMode } from "../review/diffModel";
import { CompareView, type JudgeVerdict } from "./CompareView";
import { nextDraftKey, toWireComments } from "../review/types";
import type { DraftComment } from "../review/types";
import { contextClass, formatResultCost, formatTokens, resultUsage } from "../sessions/usageFormat";
import {
  MemorySection,
  MemoryTimelineEntry,
  useMemoryDeliveries,
} from "../sessions/MemoryDeliveries";
import { t, useLocale } from "../i18n";
import "./board.css";

export interface TaskDetailApi {
  task(id: number, signal?: AbortSignal): Promise<TaskView>;
  request<T>(
    path: string,
    options?: { method?: string; body?: JsonValue },
  ): Promise<T>;
}
interface Diff {
  attempt_n: number;
  stats: { path: string; additions?: number; deletions?: number }[];
  files: { path: string; patch: string }[];
}

export function TaskDetail({
  taskId,
  api,
  onClose,
  onChat,
  onOpenSession,
  onTerminal,
  onChanged,
  onNotice,
  refreshVersion = 0,
  initialCompare = false,
}: {
  initialCompare?: boolean;
  taskId: number;
  api: TaskDetailApi;
  onClose(): void;
  onChat(t: TaskView): void;
  onOpenSession(id: number): void;
  onTerminal(url: string, title: string): void;
  onChanged(): void;
  onNotice(text: string, error?: boolean): void;
  refreshVersion?: number;
}) {
  useLocale();
  const [task, setTask] = useState<TaskView>();
  const [events, setEvents] = useState<Event[]>([]);
  const [attempt, setAttempt] = useState<number>();
  const [diff, setDiff] = useState<Diff>();
  const [diffOpen, setDiffOpen] = useState(false);
  const [compareOpen, setCompareOpen] = useState(initialCompare);
  const [judging, setJudging] = useState(false);
  const [wrap, setWrap] = useStoredFlag(PREF_KEYS.wrap, false);
  const [diffMode, setDiffMode] = useStoredPref<DiffMode>(PREF_KEYS.mode, "unified", ["unified", "split"]);
  const [busy, setBusy] = useState(false);
  const [comments, setComments] = useState<DraftComment[]>([]);
  const [reviewSummary, setReviewSummary] = useState("");
  const [reviewSending, setReviewSending] = useState(false);
  function addComment(c: Omit<DraftComment, "key">) {
    setComments((prev) => [...prev, { ...c, key: nextDraftKey() }]);
  }
  function removeComment(key: string) {
    setComments((prev) => prev.filter((c) => c.key !== key));
  }
  async function sendReview() {
    if (!task) return;
    setReviewSending(true);
    try {
      const result = await api.request<{ comments: number }>(
        `/tasks/${task.id}/review`,
        {
          method: "POST",
          body: {
            comments: toWireComments(comments) as unknown as JsonValue,
            summary: reviewSummary,
          },
        },
      );
      onNotice(
        t("board.detail.reviewSent", { n: String(result.comments ?? comments.length) }),
      );
      setComments([]);
      setReviewSummary("");
      onChanged();
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setReviewSending(false);
    }
  }
  const [steerText, setSteerText] = useState("");
  // Memory you can see (docs/memory-visibility.md): read once, used twice — the
  // section below the actions carries the feedback buttons, and the deliveries
  // themselves are entries on the timeline, where they happened.
  const memory = useMemoryDeliveries(api, "task", taskId, onNotice, true);
  async function load(signal?: AbortSignal, n = attempt) {
    const q = n ? `?attempt_n=${n}` : "";
    const [loaded, e] = await Promise.all([
      api.task(taskId, signal),
      api.request<Event[]>(`/tasks/${taskId}/events${q}`),
    ]);
    setTask(loaded);
    setEvents(e);
    setAttempt((v) => v ?? loaded.attempt?.n);
  }
  useEffect(() => {
    const c = new AbortController();
    void load(c.signal).catch(
      (e) => !c.signal.aborted && onNotice(String(e), true),
    );
    return () => c.abort();
  }, [taskId, refreshVersion]);
  useEffect(() => {
    setDiff(undefined);
    setDiffOpen(false);
    setAttempt(undefined);
    setComments([]);
    setReviewSummary("");
  }, [taskId]);
  useEffect(() => {
    const stream = new EventSource(withToken(`/api/tasks/${taskId}/stream`));
    stream.addEventListener("agent_event", (raw) => {
      try {
        const event = JSON.parse((raw as MessageEvent<string>).data) as Event;
        setEvents((old) => {
          if (attempt != null && event.attempt_n !== attempt) return old;
          if (old.some((row) => row.id === event.id)) return old;
          return [...old, event].sort((a, b) => a.seq - b.seq);
        });
        void api.task(taskId).then(setTask).catch(() => undefined);
      } catch {
        onNotice(t("board.detail.badLiveEvent"), true);
      }
    });
    return () => stream.close();
  }, [taskId, attempt]);
  async function act(name: string, body: Record<string, JsonValue> = {}) {
    setBusy(true);
    try {
      await api.request(`/tasks/${taskId}/${name}`, { method: "POST", body });
      await load();
      onChanged();
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }
  async function steer() {
    const text = steerText.trim();
    if (!text) return;
    setSteerText("");
    await act("steer", { text });
  }
  async function pick(n: number) {
    setAttempt(n);
    setEvents(await api.request(`/tasks/${taskId}/events?attempt_n=${n}`));
    setDiff(undefined);
    setDiffOpen(false);
    setComments([]);
    setReviewSummary("");
  }
  async function pickAttempt(n: number) {
    if (
      !confirm(
        t("board.detail.pickConfirm", { n }),
      )
    )
      return;
    setBusy(true);
    try {
      await api.request(`/tasks/${taskId}/pick_attempt`, {
        method: "POST",
        body: { n },
      });
      setCompareOpen(false);
      await load();
      onChanged();
      onNotice(t("board.detail.picked", { n }));
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setBusy(false);
    }
  }
  async function runJudge() {
    setJudging(true);
    try {
      await api.request(`/tasks/${taskId}/judge`, { method: "POST" });
      onNotice(t("board.detail.judgeDispatched"));
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setJudging(false);
    }
  }
  async function viewAttemptDiff(n: number) {
    await pick(n);
    setCompareOpen(false);
    try {
      setDiff(await api.request<Diff>(`/tasks/${taskId}/diff?attempt_n=${n}`));
      setDiffOpen(true);
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  async function toggleDiff() {
    if (diffOpen) {
      setDiffOpen(false);
      return;
    }
    try {
      setDiff(
        await api.request<Diff>(
          `/tasks/${taskId}/diff${attempt ? `?attempt_n=${attempt}` : ""}`,
        ),
      );
      setDiffOpen(true);
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  if (!task)
    return (
      <Modal id="sheet" open className="sheet" aria-label={t("board.detail.loadingLabel")} onCancel={onClose}>
        <p aria-busy="true">{t("board.detail.loading")}</p>
      </Modal>
    );
  const takeover = task.takeover;
  return (
    <Modal id="sheet" open className="sheet task-detail" aria-label={t("board.detail.label")} onCancel={onClose}>
      <header className="sheet-head">
        <h2>{task.title}</h2>
        <button className="x" onClick={onClose}>✕</button>
      </header>
      <div className={`statline s-${task.status}`}>
        <span className={`statpill s-${task.status}`}>{t(`board.status.${task.status}`, undefined, task.status)}</span> · {task.project_name} → {task.target_name}
        {task.attempt && (
          <>
            {" "}
            · {t("board.compare.attemptN", { n: task.attempt.n })}
            {task.attempt.branch && (
              <>
                {" "}
                · <code>{task.attempt.branch}</code>
              </>
            )}
          </>
        )}
      </div>
      {task.limit && (
        <LimitBanner
          hold={task.limit}
          api={api}
          onNotice={onNotice}
          onRefresh={async () => {
            setTask(await api.task(taskId));
          }}
        />
      )}
      {task.attempt?.result && (() => {
        const u = resultUsage(task.attempt!.result);
        if (u.costUSD == null && u.outputTokens == null && u.contextPct == null) return null;
        return (
          <div className="usage-line">
            {u.costUSD != null && (
                        <span className="chip cost" title={u.costEstimated ? t("board.usage.costEstimated") : undefined}>
                          {formatResultCost(u)}
                        </span>
                      )}
            {u.costUSD == null && u.outputTokens != null && (
              <span className="chip">{t("board.usage.tokens", { tokens: formatTokens(u.outputTokens) })}</span>
            )}
            {u.contextPct != null && (
              <span
                className={`ctxbar ctx-used ${contextClass(u.contextPct)}`}
                title={t("board.usage.contextUsed", { used: formatTokens(u.contextTokens), size: formatTokens(u.contextSize) })}
              >
                {t("board.usage.ctx")} <i><b style={{ width: `${u.contextPct}%` }} /></i> {u.contextPct}%
              </span>
            )}
          </div>
        );
      })()}
      {task.attempt?.worktree_path && task.status !== "done" && (
        <div className="btnrow">
          <OpenInEditor targetId={task.target_id} path={task.attempt.worktree_path} />
        </div>
      )}
      {task.attempts.length > 1 && (
        <div className="btnrow attempt-chips">
          {task.attempts.map((a) => (
            <button
              className={attempt === a.n && !compareOpen ? "b ok" : "b"}
              key={a.n}
              onClick={() => {
                setCompareOpen(false);
                void pick(a.n);
              }}
            >
              {t("board.detail.attemptShort", { n: a.n })}
              {a.model && ` · ${a.model}`} · {t(`board.status.${a.status}`, undefined, a.status)}
              {a.cost_usd != null && ` · $${Number(a.cost_usd).toFixed(2)}`}
            </button>
          ))}
          <button
            className={compareOpen ? "b ok" : "b"}
            onClick={() => setCompareOpen((v) => !v)}
          >
            {t("board.detail.compare")}
          </button>
        </div>
      )}
      {compareOpen && task.attempts.length > 1 && (
        <CompareView
          attempts={task.attempts}
          judgment={task.attempt?.result?.judge as JudgeVerdict | undefined}
          busy={busy}
          judging={judging}
          onViewDiff={(n) => void viewAttemptDiff(n)}
          onPick={(n) => void pickAttempt(n)}
          onJudge={() => void runJudge()}
        />
      )}
      <div id="actions" className="btnrow actions">
        <button
          className="b ok grow"
          onClick={() =>
            takeover?.status === "ready" && takeover.session_id
              ? onOpenSession(takeover.session_id)
              : onChat(task)
          }
        >
          {takeover?.status === "ready" ? t("board.detail.openSession") : t("board.card.chat")}
        </button>
        {takeover?.status === "failed" && (
          <button className="b warn" onClick={() => void act("takeover")}>
            {t("board.detail.retryTakeover")}
          </button>
        )}
        {!takeover &&
          task.attempt?.worktree_path &&
          ["running", "review", "failed", "cancelled", "done"].includes(
            task.status,
          ) &&
          task.target_kind !== "sandbox" && (
            <button className="b ok" onClick={() => void act("takeover")}>
              {t("board.detail.takeOver")}
            </button>
          )}
        {!takeover &&
          ["backlog", "failed", "cancelled"].includes(task.status) && (
            <button
              className="b ok"
              disabled={busy}
              onClick={() => void act("dispatch")}
            >
              {task.status === "backlog" ? t("board.detail.dispatch") : t("board.detail.retry")}
            </button>
          )}
        {!takeover && ["queued", "running"].includes(task.status) && (
          <button
            className="b no"
            disabled={busy}
            onClick={() => void act("cancel")}
          >
            {t("board.detail.cancel")}
          </button>
        )}
        {!takeover && task.status === "review" && (
          <>
            <button className="b ok" onClick={() => void act("complete")}>
              {t("board.detail.markDone")}
            </button>
            <button
              className="b warn"
              onClick={() => {
                const feedback = prompt(t("board.detail.whatShouldChange"));
                if (feedback) void act("followup", { feedback });
              }}
            >
              {t("board.detail.requestChanges")}
            </button>
          </>
        )}
        {["review", "done"].includes(task.status) && (
          <>
            <button className="b" onClick={() => void toggleDiff()}>
              {diffOpen ? t("board.detail.timeline") : t("board.detail.diff")}
            </button>
            <button
              className="b"
              onClick={() => {
                const message = prompt(t("board.detail.commitMessage"), task.title);
                if (message == null) return;
                const push = confirm(t("board.detail.pushConfirm"));
                const pr =
                  push && confirm(t("board.detail.prConfirm"));
                void act("commit", { message, push, pr });
              }}
            >
              {t("board.detail.commit")}
            </button>
          </>
        )}
        {!takeover &&
          ["done", "failed", "cancelled"].includes(task.status) &&
          task.attempt?.worktree_path && (
            <button
              className="b"
              onClick={() =>
                confirm(
                  t("board.detail.cleanConfirm"),
                ) && void act("cleanup")
              }
            >
              {t("board.detail.clean")}
            </button>
          )}
        {task.status === "running" && task.attempt?.tmux_session && (
          <button
            className="b"
            onClick={() =>
              void api
                .request<{ url: string; notice?: string }>(`/tasks/${task.id}/terminal`, {
                  method: "POST",
                })
                .then((r) => {
                  onTerminal(r.url, task.title);
                  if (r.notice) onNotice(r.notice);
                })
                .catch((e) => onNotice(String(e), true))
            }
          >
            {t("board.detail.terminal")}
          </button>
        )}
        <button
          className="b no"
          onClick={() =>
            confirm(
              t("board.detail.deleteConfirm", { title: task.title }),
            ) &&
            void api
              .request(`/tasks/${task.id}`, { method: "DELETE" })
              .then(() => {
                onChanged();
                onClose();
              })
          }
        >
          {t("board.detail.delete")}
        </button>
      </div>
      <MemorySection state={memory} />
      {task.status === "running" && task.attempt?.driver === "claude-steer" && (
        <form
          className="steer-row"
          onSubmit={(e) => {
            e.preventDefault();
            void steer();
          }}
        >
          <input
            type="text"
            className="steer-input"
            placeholder={t("board.detail.steerPlaceholder")}
            value={steerText}
            onChange={(e) => setSteerText(e.target.value)}
          />
          <button className="b ok" type="submit" disabled={busy || !steerText.trim()}>
            {t("board.detail.send")}
          </button>
        </form>
      )}
      {takeover && (
        <p className="subhint">
          {takeover.status === "ready"
            ? t("board.detail.takeoverReady")
            : takeover.status === "failed"
              ? takeover.error
              : t("board.detail.takingOver")}
        </p>
      )}
      {!diffOpen ? (
        <div className="tl">
          {memory.deliveries?.map((delivery) => (
            <MemoryTimelineEntry
              key={delivery.id}
              delivery={delivery}
              attempt={
                task.attempts.length > 1
                  ? task.attempts.find((a) => a.id === delivery.attempt_id)?.n
                  : undefined
              }
            />
          ))}
          {events.length === 0 && task.prompt && (
            <article className="ev">
              <div className="k">{t("board.detail.promptLabel")}</div>
              <pre>{task.prompt}</pre>
            </article>
          )}
          {events.map((e) => (
            <EventRow key={e.id || `${e.attempt_n}-${e.seq}`} event={e} />
          ))}
        </div>
      ) : (
        <>
          <header className="diffhead">
            {t("board.detail.diffHead", { n: diff?.attempt_n ?? "", files: diff?.stats.length ?? 0 })}{" "}
            <DiffModeToggle mode={diffMode} onChange={setDiffMode} />
            <button
              className={"wrapbtn" + (wrap ? " on" : "")}
              aria-pressed={wrap}
              onClick={() => setWrap(!wrap)}
            >
              {t("board.detail.wrap", { state: wrap ? t("board.detail.wrapOn") : t("board.detail.wrapOff") })}
            </button>
          </header>
          <DiffViewer
            files={diff?.files ?? []}
            stats={diff?.stats ?? []}
            wrap={wrap}
            mode={diffMode}
            commentable={task.status === "review"}
            comments={comments}
            onAddComment={addComment}
          />
          {task.status === "review" && (
            <CommentTray
              comments={comments}
              summary={reviewSummary}
              onSummaryChange={setReviewSummary}
              onRemove={removeComment}
              onSend={() => void sendReview()}
              busy={reviewSending}
              sendLabel={t("board.detail.sendReview")}
            />
          )}
        </>
      )}
    </Modal>
  );
}

function EventRow({ event }: { event: Event }) {
  useLocale();
  const p = event.payload;
  const label =
    event.type === "init"
      ? t("board.event.sessionStart")
      : event.type === "text"
        ? t("board.event.agent")
        : event.type === "tool_use"
          ? t("board.event.tool")
          : event.type === "tool_result"
            ? t("board.event.result") + (p.is_error ? t("board.event.error") : "")
            : event.type === "verify"
              ? t("board.event.autoVerify", { outcome: p.rc === 0 ? t("board.event.pass") : t("board.event.fail") })
              : event.type === "review_verdict"
                ? t("board.event.reviewerVerdict", { verdict: String(p.verdict ?? "") })
                : event.type === "result"
                  ? t("board.event.finished", { subtype: String(p.subtype ?? "") })
                  : event.type === "rate_limit"
                    ? t("board.event.usageLimit")
                    : event.type;
  const body =
    event.type === "text"
      ? p.text
      : event.type === "tool_use"
        ? `${String(p.name ?? "")} ${JSON.stringify(p.input ?? {}).slice(0, 120)}`
        : event.type === "tool_result"
          ? String(p.content ?? "").slice(0, 400)
          : event.type === "verify"
            ? `${String(p.cmd ?? "")}\n${String(p.output ?? "").slice(-500)}`
            : event.type === "review_verdict"
              ? String(p.notes ?? "").slice(-400)
              : event.type === "result"
                ? String(p.result ?? "")
                : event.type === "error"
                  ? String(p.message ?? "")
                  : event.type === "rate_limit"
                    ? t("board.event.stoppedByLimit") + (p.resets_at ? t("board.event.resets", { when: new Date(Number(p.resets_at) * 1000).toLocaleString() }) : "")
                    : JSON.stringify(p).slice(0, 300);
  return (
    <article className={`ev e-${event.type}`}>
      <div className="k">{label}</div>
      <pre className="body">{String(body ?? "")}</pre>
    </article>
  );
}
