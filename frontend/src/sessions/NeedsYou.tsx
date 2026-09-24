import { useEffect, useMemo, useState } from "react";
import type { Approval, SessionView, TaskView } from "../types";
import type { SessionsApi } from "./Sessions";
import { ContextBadge, CostBadge, LinesBadge } from "./UsageBadges";
import "./session-home.css";

// What actually wants a person, drawn only from state the server already
// records: a pending approval, a session that says it is waiting, a session
// whose setup failed, a task that failed, and work sitting in review. An idle
// session is left in the list below — quiet is not the same as blocked.

interface Props {
  api: SessionsApi;
  rows: SessionView[];
  refreshVersion?: number;
  onChat(session: SessionView): void;
  onAttach(session: SessionView): void;
  onReview(session: SessionView): void;
  onShowSession(session: SessionView): void;
  onOpenTask?(id: number): void;
  onChanged?(): void;
  onNotice(text: string, error?: boolean): void;
}

type Item =
  | { key: string; reason: "approval"; approval: Approval }
  | { key: string; reason: "waiting" | "failed-session" | "stopped-session"; session: SessionView }
  | { key: string; reason: "failed-task" | "review-task"; task: TaskView };

const REASON: Record<Item["reason"], string> = {
  approval: "Approval needed",
  waiting: "Wants you",
  "failed-session": "Setup failed",
  "stopped-session": "Session ended",
  "failed-task": "Task failed",
  "review-task": "Ready to review",
};
const RANK: Record<Item["reason"], number> = {
  approval: 0,
  waiting: 1,
  "failed-session": 2,
  "stopped-session": 3,
  "failed-task": 4,
  "review-task": 5,
};
const CAP = 8;

export function NeedsYou({
  api,
  rows,
  refreshVersion = 0,
  onChat,
  onAttach,
  onReview,
  onShowSession,
  onOpenTask,
  onChanged,
  onNotice,
}: Props) {
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [tasks, setTasks] = useState<TaskView[]>([]);
  const [busy, setBusy] = useState("");
  const [stale, setStale] = useState(false);
  const [showAll, setShowAll] = useState(false);
  useEffect(() => {
    const abort = new AbortController();
    // Attention is derived, never invented: a failed poll hides the row rather
    // than claiming work is waiting when it cannot be checked. What a failed
    // poll must not do is claim that nothing needs a person, so the last known
    // rows are kept and marked stale.
    const load = async () => {
      if (document.hidden) return;
      const [pending, failing, reviewing] = await Promise.allSettled([
        api.request<Approval[]>("/approvals?status=pending", {
          signal: abort.signal,
        }),
        api.request<TaskView[]>("/tasks?status=failed", {
          signal: abort.signal,
        }),
        api.request<TaskView[]>("/tasks?status=review", {
          signal: abort.signal,
        }),
      ]);
      if (abort.signal.aborted) return;
      setStale([pending, failing, reviewing].some((row) => row.status === "rejected"));
      if (pending.status === "fulfilled" && Array.isArray(pending.value))
        setApprovals(pending.value);
      if (
        failing.status === "fulfilled" &&
        reviewing.status === "fulfilled" &&
        Array.isArray(failing.value) &&
        Array.isArray(reviewing.value)
      )
        setTasks([...failing.value, ...reviewing.value]);
    };
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    const visible = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      abort.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [api, refreshVersion]);

  const items = useMemo<Item[]>(() => {
    const reviewTasks = tasks.filter((task) => task.status === "review");
    const failedTasks = tasks.filter((task) => task.status === "failed");
    const approvalTasks = new Set(
      approvals.map((approval) => approval.task_id).filter((id): id is number => !!id),
    );
    // A session that took over a task which is now asking for approval is
    // covered by that approval; one row, not two descriptions of the same wait.
    const covered = new Set(
      tasks
        .filter((task) => task.takeover?.session_id && approvalTasks.has(task.id))
        .map((task) => task.takeover!.session_id!),
    );
    const out: Item[] = approvals.map((approval) => ({
      key: `approval-${approval.id}`,
      reason: "approval",
      approval,
    }));
    for (const session of rows) {
      if (session.archived_at != null || session.ended_at != null) continue;
      if (session.setup_state === "failed") {
        out.push({ key: `session-${session.id}`, reason: "failed-session", session });
      } else if (session.status === "waiting" && !covered.has(session.id)) {
        out.push({ key: `session-${session.id}`, reason: "waiting", session });
      } else if (session.status === "dead" && session.origin !== "discovered") {
        out.push({ key: `session-${session.id}`, reason: "stopped-session", session });
      }
    }
    for (const task of failedTasks)
      out.push({ key: `task-${task.id}`, reason: "failed-task", task });
    for (const task of reviewTasks)
      out.push({ key: `task-${task.id}`, reason: "review-task", task });
    return out.sort((a, b) => RANK[a.reason] - RANK[b.reason]);
  }, [approvals, tasks, rows]);

  async function decide(approval: Approval, decision: "approved" | "denied") {
    setBusy(`approval-${approval.id}`);
    try {
      await api.request(`/approvals/${approval.id}/decision`, {
        method: "POST",
        body: { decision },
      });
      setApprovals((old) => old.filter((row) => row.id !== approval.id));
      onChanged?.();
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  // Nothing to do, and we know it: stay out of the way. A failed refresh with
  // no known rows must still say so rather than look like an all-clear.
  if (!items.length && !stale) return null;
  const shown = showAll ? items : items.slice(0, CAP);
  return (
    <section
      className="needs-you"
      id="needs-you"
      aria-label="Needs you"
      data-stale={stale ? "true" : undefined}
    >
      <header>
        <h3>Needs you</h3>
        {items.length > 0 && <span className="ny-count">{items.length}</span>}
        {stale && (
          <span className="ny-stale" id="needs-you-stale" role="status">
            Couldn’t refresh — showing the last known state
          </span>
        )}
      </header>
      <ul className="ny-list">
        {shown.map((item) => (
          <li
            className="ny-row"
            key={item.key}
            data-reason={item.reason}
            data-id={item.key}
            data-stale={stale ? "true" : undefined}
          >
            <div className="ny-why">
              <span className="ny-reason">{REASON[item.reason]}</span>
              <span className="ny-what">
                {"approval" in item
                  ? item.approval.task_title || `Attempt ${item.approval.attempt_id}`
                  : "session" in item
                    ? item.session.name
                    : item.task.title}
              </span>
              <span className="ny-where">{describe(item)}</span>
              {"session" in item && (
                <div className="ny-usage">
                  {item.session.model && <span className="chip">{item.session.model}</span>}
                  <ContextBadge session={item.session} />
                  <CostBadge session={item.session} />
                  <LinesBadge session={item.session} />
                </div>
              )}
            </div>
            <div className="ny-actions">{actions(item)}</div>
          </li>
        ))}
      </ul>
      {items.length > CAP && (
        <button
          type="button"
          className="b ny-toggle"
          id="needs-you-toggle"
          aria-expanded={showAll}
          onClick={() => setShowAll((open) => !open)}
        >
          {showAll ? "Show fewer" : `Show all ${items.length}`}
        </button>
      )}
    </section>
  );

  function describe(item: Item) {
    if ("approval" in item)
      return [item.approval.tool_name, approvalDetail(item.approval)]
        .filter(Boolean)
        .join(" · ");
    if ("session" in item) {
      if (item.reason === "failed-session")
        return item.session.setup_error || "workspace setup did not finish";
      if (item.reason === "stopped-session") return "its terminal is gone";
      return [item.session.agent, item.session.project_name || "unassigned"]
        .filter(Boolean)
        .join(" · ");
    }
    return [item.task.project_name, item.task.agent].filter(Boolean).join(" · ");
  }

  function taskAction(id: number, label: string) {
    return onOpenTask ? (
      <button className="b" onClick={() => onOpenTask(id)}>
        {label}
      </button>
    ) : (
      <a className="b link-button" href={`#task/${id}`}>
        {label}
      </a>
    );
  }

  function actions(item: Item) {
    if ("approval" in item) {
      const { approval } = item;
      return (
        <>
          <button
            className="b ok"
            disabled={busy === `approval-${approval.id}`}
            onClick={() => void decide(approval, "approved")}
          >
            Approve
          </button>
          <button
            className="b"
            disabled={busy === `approval-${approval.id}`}
            onClick={() => void decide(approval, "denied")}
          >
            Deny
          </button>
          {!!approval.task_id && taskAction(approval.task_id, "Open task")}
        </>
      );
    }
    if ("session" in item) {
      if (item.reason !== "waiting") {
        return (
          <button className="b" onClick={() => onShowSession(item.session)}>
            Show session
          </button>
        );
      }
      if (item.session.agent === "shell") {
        // A shell sits at its prompt by nature; chatting with it is not the way
        // to help it, the same as on its card.
        return (
          <>
            <button className="b ok ny-terminal" onClick={() => onAttach(item.session)}>
              ⌨ Terminal
            </button>
            <button className="b" onClick={() => onReview(item.session)}>
              Review
            </button>
          </>
        );
      }
      return (
        <>
          <button className="b ok ny-chat" onClick={() => onChat(item.session)}>
            Chat
          </button>
          <button className="b" onClick={() => onAttach(item.session)}>
            ⌨ Terminal
          </button>
          <button className="b" onClick={() => onReview(item.session)}>
            Review
          </button>
        </>
      );
    }
    return taskAction(
      item.task.id,
      item.reason === "review-task" ? "Review" : "Open task",
    );
  }
}

function approvalDetail(approval: Approval) {
  const input = approval.input || {};
  for (const field of ["command", "path", "file_path", "url", "pattern"]) {
    const value = input[field];
    if (typeof value === "string" && value.trim()) return value.trim().slice(0, 90);
  }
  const text = JSON.stringify(input);
  return text && text !== "{}" ? text.slice(0, 90) : "";
}
