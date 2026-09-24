import { useEffect, useState } from "react";
import type { Event, TaskView } from "../types";
import { withToken, type JsonValue } from "../api";
import { Modal } from "../sessions/Modal";
import { DiffViewer } from "../review/DiffViewer";
import { CommentTray } from "../review/CommentTray";
import { nextDraftKey, toWireComments } from "../review/types";
import type { DraftComment } from "../review/types";
import { contextClass, formatCost, formatTokens, resultUsage } from "../sessions/usageFormat";
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
}: {
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
  const [task, setTask] = useState<TaskView>();
  const [events, setEvents] = useState<Event[]>([]);
  const [attempt, setAttempt] = useState<number>();
  const [diff, setDiff] = useState<Diff>();
  const [diffOpen, setDiffOpen] = useState(false);
  const [wrap, setWrap] = useState(
    localStorage.getItem("lec-diffwrap") === "1",
  );
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
        `Sent ${String(result.comments ?? comments.length)} comment(s) as request-changes feedback.`,
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
  async function load(signal?: AbortSignal, n = attempt) {
    const q = n ? `?attempt_n=${n}` : "";
    const [t, e] = await Promise.all([
      api.task(taskId, signal),
      api.request<Event[]>(`/tasks/${taskId}/events${q}`),
    ]);
    setTask(t);
    setEvents(e);
    setAttempt((v) => v ?? t.attempt?.n);
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
        onNotice("Could not read a live task event.", true);
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
  async function pick(n: number) {
    setAttempt(n);
    setEvents(await api.request(`/tasks/${taskId}/events?attempt_n=${n}`));
    setDiff(undefined);
    setDiffOpen(false);
    setComments([]);
    setReviewSummary("");
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
      <Modal id="sheet" open className="sheet" aria-label="Loading task" onCancel={onClose}>
        <p aria-busy="true">Loading task…</p>
      </Modal>
    );
  const takeover = task.takeover;
  return (
    <Modal id="sheet" open className="sheet task-detail" aria-label="Task details" onCancel={onClose}>
      <header className="sheet-head">
        <h2>{task.title}</h2>
        <button className="x" onClick={onClose}>✕</button>
      </header>
      <div className={`statline s-${task.status}`}>
        <span className={`statpill s-${task.status}`}>{task.status}</span> · {task.project_name} → {task.target_name}
        {task.attempt && (
          <>
            {" "}
            · attempt #{task.attempt.n}
            {task.attempt.branch && (
              <>
                {" "}
                · <code>{task.attempt.branch}</code>
              </>
            )}
          </>
        )}
      </div>
      {task.attempt?.result && (() => {
        const u = resultUsage(task.attempt!.result);
        if (u.costUSD == null && u.outputTokens == null && u.contextPct == null) return null;
        return (
          <div className="usage-line">
            {u.costUSD != null && <span className="chip cost">{formatCost(u.costUSD)}</span>}
            {u.costUSD == null && u.outputTokens != null && (
              <span className="chip">{formatTokens(u.outputTokens)} tok</span>
            )}
            {u.contextPct != null && (
              <span
                className={`ctxbar ctx-used ${contextClass(u.contextPct)}`}
                title={`${formatTokens(u.contextTokens)} / ${formatTokens(u.contextSize)} tokens used`}
              >
                ctx <i><b style={{ width: `${u.contextPct}%` }} /></i> {u.contextPct}%
              </span>
            )}
          </div>
        );
      })()}
      {task.attempts.length > 1 && (
        <div className="btnrow attempt-chips">
          {task.attempts.map((a) => (
            <button
              className={attempt === a.n ? "b ok" : "b"}
              key={a.n}
              onClick={() => void pick(a.n)}
            >
              ⑂ A{a.n}
              {a.model && ` · ${a.model}`} · {a.status}
              {a.cost_usd != null && ` · $${Number(a.cost_usd).toFixed(2)}`}
            </button>
          ))}
        </div>
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
          {takeover?.status === "ready" ? "Open session" : "Chat"}
        </button>
        {takeover?.status === "failed" && (
          <button className="b warn" onClick={() => void act("takeover")}>
            Retry takeover
          </button>
        )}
        {!takeover &&
          task.attempt?.worktree_path &&
          ["running", "review", "failed", "cancelled", "done"].includes(
            task.status,
          ) &&
          task.target_kind !== "sandbox" && (
            <button className="b ok" onClick={() => void act("takeover")}>
              Take over as session
            </button>
          )}
        {!takeover &&
          ["backlog", "failed", "cancelled"].includes(task.status) && (
            <button
              className="b ok"
              disabled={busy}
              onClick={() => void act("dispatch")}
            >
              {task.status === "backlog" ? "▶ Dispatch" : "↻ Retry"}
            </button>
          )}
        {!takeover && ["queued", "running"].includes(task.status) && (
          <button
            className="b no"
            disabled={busy}
            onClick={() => void act("cancel")}
          >
            ■ Cancel
          </button>
        )}
        {!takeover && task.status === "review" && (
          <>
            <button className="b ok" onClick={() => void act("complete")}>
              ✓ Mark done
            </button>
            <button
              className="b warn"
              onClick={() => {
                const feedback = prompt("What should change?");
                if (feedback) void act("followup", { feedback });
              }}
            >
              ↺ Request changes
            </button>
          </>
        )}
        {["review", "done"].includes(task.status) && (
          <>
            <button className="b" onClick={() => void toggleDiff()}>
              {diffOpen ? "Timeline" : "± Diff"}
            </button>
            <button
              className="b"
              onClick={() => {
                const message = prompt("Commit message:", task.title);
                if (message == null) return;
                const push = confirm("Also push the branch to origin?");
                const pr =
                  push && confirm("…and open a PR (needs gh on the target)?");
                void act("commit", { message, push, pr });
              }}
            >
              ⎇ Commit
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
                  "Remove the worktree(s)? Uncommitted changes are lost.",
                ) && void act("cleanup")
              }
            >
              Clean worktree
            </button>
          )}
        {task.status === "running" && task.attempt?.tmux_session && (
          <button
            className="b"
            onClick={() =>
              void api
                .request<{ url: string }>(`/tasks/${task.id}/terminal`, {
                  method: "POST",
                })
                .then((r) => onTerminal(r.url, task.title))
                .catch((e) => onNotice(String(e), true))
            }
          >
            ⌨ Terminal
          </button>
        )}
        <button
          className="b no"
          onClick={() =>
            confirm(
              `Delete “${task.title}”? Removes its attempts, events, diffs and worktrees. Cannot be undone.`,
            ) &&
            void api
              .request(`/tasks/${task.id}`, { method: "DELETE" })
              .then(() => {
                onChanged();
                onClose();
              })
          }
        >
          Delete
        </button>
      </div>
      {takeover && (
        <p className="subhint">
          {takeover.status === "ready"
            ? "Continued in an interactive session."
            : takeover.status === "failed"
              ? takeover.error
              : "Taking over this run… Its worktree is preserved."}
        </p>
      )}
      {!diffOpen ? (
        <div className="tl">
          {events.length === 0 && task.prompt && (
            <article className="ev">
              <div className="k">prompt</div>
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
            attempt #{diff?.attempt_n} · {diff?.stats.length ?? 0} file(s)
            changed{" "}
            <button
              className={"wrapbtn" + (wrap ? " on" : "")}
              onClick={() => {
                const n = !wrap;
                setWrap(n);
                localStorage.setItem("lec-diffwrap", n ? "1" : "");
              }}
            >
              ⏎ wrap: {wrap ? "on" : "off"}
            </button>
          </header>
          <DiffViewer
            files={diff?.files ?? []}
            stats={diff?.stats ?? []}
            wrap={wrap}
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
              sendLabel="Send as request-changes feedback"
            />
          )}
        </>
      )}
    </Modal>
  );
}

function EventRow({ event }: { event: Event }) {
  const p = event.payload;
  const label =
    event.type === "init"
      ? "session start"
      : event.type === "text"
        ? "agent"
        : event.type === "tool_use"
          ? "tool"
          : event.type === "tool_result"
            ? `↳ result${p.is_error ? " · ERROR" : ""}`
            : event.type === "verify"
              ? `auto-verify · ${p.rc === 0 ? "PASS ✓" : "FAIL ✗"}`
              : event.type === "review_verdict"
                ? `reviewer verdict · ${String(p.verdict ?? "")}`
                : event.type === "result"
                  ? `finished · ${String(p.subtype ?? "")}`
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
                : JSON.stringify(p).slice(0, 300);
  return (
    <article className={`ev e-${event.type}`}>
      <div className="k">{label}</div>
      <pre className="body">{String(body ?? "")}</pre>
    </article>
  );
}
