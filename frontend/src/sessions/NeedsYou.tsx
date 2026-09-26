import { useEffect, useMemo, useState } from "react";
import type { Approval, DuplicatePromptPair, SessionView, TaskView } from "../types";
import type { SessionsApi } from "./Sessions";
import { ContextBadge, CostBadge, LinesBadge } from "./UsageBadges";
import { approvalSummary } from "./approval-summary";
import { useDictation } from "../voice";
import "./session-home.css";

// What actually wants a person, drawn only from state the server already
// records: a pending approval, a session that says it is waiting, a session
// whose setup failed, a task that failed, and work sitting in review. An idle
// session is left in the list below — quiet is not the same as blocked.

// PushPrompt is the one-time, dismissible "enable phone alerts" nudge shown
// here when push is available in this browser but this device has never
// subscribed. App.tsx owns the actual availability check and subscription
// state (both require live browser APIs); this component only renders what
// it is handed and reports the two things a person can do with it.
export interface PushPrompt {
  show: boolean;
  onEnable(): void;
  onDismiss(): void;
}

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
  pushPrompt?: PushPrompt;
}

type Item =
  | { key: string; reason: "approval"; approval: Approval }
  | { key: string; reason: "waiting" | "failed-session" | "stopped-session"; session: SessionView }
  | { key: string; reason: "failed-task" | "review-task"; task: TaskView }
  | { key: string; reason: "duplicate-work"; pair: DuplicatePromptPair };

const REASON: Record<Item["reason"], string> = {
  approval: "Approval needed",
  waiting: "Wants you",
  "failed-session": "Setup failed",
  "stopped-session": "Session ended",
  "failed-task": "Task failed",
  "review-task": "Ready to review",
  "duplicate-work": "Possible duplicate work",
};
const RANK: Record<Item["reason"], number> = {
  approval: 0,
  waiting: 1,
  "failed-session": 2,
  "stopped-session": 3,
  "failed-task": 4,
  "review-task": 5,
  "duplicate-work": 6,
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
  pushPrompt,
}: Props) {
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [tasks, setTasks] = useState<TaskView[]>([]);
  const [duplicates, setDuplicates] = useState<DuplicatePromptPair[]>([]);
  const [busy, setBusy] = useState("");
  const [stale, setStale] = useState(false);
  const [showAll, setShowAll] = useState(false);
  // Which approval's "deny with reason" field is open, keyed by approval id.
  const [denying, setDenying] = useState<number | null>(null);
  const [reason, setReason] = useState("");
  const { supported: dictationSupported, dictating, toggle: toggleDictation } = useDictation({
    onChange: setReason,
    onNotice,
  });
  useEffect(() => {
    const abort = new AbortController();
    // Attention is derived, never invented: a failed poll hides the row rather
    // than claiming work is waiting when it cannot be checked. What a failed
    // poll must not do is claim that nothing needs a person, so the last known
    // rows are kept and marked stale.
    const load = async () => {
      if (document.hidden) return;
      const [pending, failing, reviewing, dupes] = await Promise.allSettled([
        api.request<Approval[]>("/approvals?status=pending", {
          signal: abort.signal,
        }),
        api.request<TaskView[]>("/tasks?status=failed", {
          signal: abort.signal,
        }),
        api.request<TaskView[]>("/tasks?status=review", {
          signal: abort.signal,
        }),
        // Cross-agent awareness (docs/agent-events.md point 6): a lighter,
        // best-effort signal, so its failure does NOT mark the whole section
        // stale — a homelab running an older lectern build without this
        // endpoint should not show "couldn't refresh" forever.
        api.request<DuplicatePromptPair[]>("/awareness/duplicate-prompts", {
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
      if (dupes.status === "fulfilled" && Array.isArray(dupes.value)) setDuplicates(dupes.value);
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
    for (const pair of duplicates)
      out.push({
        key: `dup-${pair.session_a_id}-${pair.session_b_id}`,
        reason: "duplicate-work",
        pair,
      });
    return out.sort((a, b) => RANK[a.reason] - RANK[b.reason]);
  }, [approvals, tasks, duplicates, rows]);

  async function decide(approval: Approval, decision: "approved" | "denied", note?: string) {
    setBusy(`approval-${approval.id}`);
    try {
      await api.request(`/approvals/${approval.id}/decision`, {
        method: "POST",
        body: note ? { decision, note } : { decision },
      });
      setApprovals((old) => old.filter((row) => row.id !== approval.id));
      setDenying(null);
      setReason("");
      onChanged?.();
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy("");
    }
  }

  // Board tasks sitting in review are not sessions and do not wait on you in
  // the way a blocked session does; with an autonomous workshop producing a
  // steady stream of them they buried the few sessions that did. They get one
  // line that leads to the Board's Review column instead of a row each.
  const reviewCount = tasks.filter((task) => task.status === "review").length;
  const showPrompt = !!pushPrompt?.show;
  // Nothing to do, and we know it: stay out of the way. A failed refresh with
  // no known rows must still say so rather than look like an all-clear. The
  // push prompt is the one thing that can keep the section open with zero
  // items — it is a suggestion, not something that "needs" attention, but it
  // belongs where a person is already looking.
  if (!items.length && !stale && !showPrompt && !reviewCount) return null;
  const shown = showAll ? items : items.slice(0, CAP);
  return (
    <section
      className="needs-you"
      id="needs-you"
      aria-label="Needs you"
      data-stale={stale ? "true" : undefined}
    >
      {showPrompt && (
        <div className="ny-push-prompt" id="needs-you-push-prompt" role="status">
          <span>Get a phone alert when a session needs you.</span>
          <div className="ny-push-prompt-actions">
            <button className="b ok" id="needs-you-push-enable" onClick={pushPrompt!.onEnable}>
              Enable phone alerts
            </button>
            <button
              className="b"
              id="needs-you-push-dismiss"
              aria-label="Dismiss phone alerts prompt"
              onClick={pushPrompt!.onDismiss}
            >
              Not now
            </button>
          </div>
        </div>
      )}
      {(items.length > 0 || stale) && (
        <>
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
                      ? item.approval.session_name ||
                        item.approval.task_title ||
                        `Attempt ${item.approval.attempt_id}`
                      : "session" in item
                        ? item.session.name
                        : "pair" in item
                          ? `${item.pair.session_a} & ${item.pair.session_b}`
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
        </>
      )}
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
      {reviewCount > 0 && (
        <a className="ny-review-link" id="needs-you-review-link" href="#board">
          {reviewCount} {reviewCount === 1 ? "task" : "tasks"} ready to review
          <span aria-hidden="true"> → Board</span>
        </a>
      )}
    </section>
  );

  function describe(item: Item) {
    if ("approval" in item)
      return [item.approval.tool_name, approvalSummary(item.approval)]
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
    if ("pair" in item)
      return `Their last prompts overlap ${Math.round(item.pair.score * 100)}% — check they aren't building the same thing`;
    return [item.task.project_name, item.task.agent].filter(Boolean).join(" · ");
  }

  // A session approval's deep link opens the session itself, the same way a
  // "waiting" row's actions do — found from the rows this component was
  // already handed, so this needs no extra fetch. A row not present here
  // (rare — the session list and the approval poll are on separate ticks)
  // degrades to a plain hash link rather than disappearing.
  function sessionAction(sessionID: number, label = "Open session") {
    const session = rows.find((row) => row.id === sessionID);
    if (session)
      return (
        <button className="b" onClick={() => onShowSession(session)}>
          {label}
        </button>
      );
    return (
      <a className="b link-button" href={`#session/${sessionID}`}>
        {label}
      </a>
    );
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
      const isBusy = busy === `approval-${approval.id}`;
      if (denying === approval.id) {
        return (
          <form
            className="ny-deny-reason"
            onSubmit={(e) => {
              e.preventDefault();
              void decide(approval, "denied", reason.trim() || undefined);
            }}
          >
            <input
              autoFocus
              placeholder="Reason (optional)"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              aria-label="Reason for denying"
            />
            {dictationSupported && (
              <button
                type="button"
                className={dictating ? "b mic-recording" : "b"}
                id="needs-you-deny-mic"
                aria-label={dictating ? "Stop dictating" : "Dictate reason"}
                aria-pressed={dictating}
                onClick={() => toggleDictation(reason)}
              >
                {dictating ? "🔴" : "🎙"}
              </button>
            )}
            <button className="b" type="submit" disabled={isBusy}>
              Send
            </button>
            <button
              className="b"
              type="button"
              onClick={() => {
                setDenying(null);
                setReason("");
              }}
            >
              Cancel
            </button>
          </form>
        );
      }
      return (
        <>
          <button className="b ok" disabled={isBusy} onClick={() => void decide(approval, "approved")}>
            Approve
          </button>
          <button className="b" disabled={isBusy} onClick={() => void decide(approval, "denied")}>
            Deny
          </button>
          <button
            className="b ny-deny-more"
            disabled={isBusy}
            onClick={() => {
              setDenying(approval.id);
              setReason("");
            }}
          >
            Deny with reason…
          </button>
          {!!approval.session_id && sessionAction(approval.session_id)}
          {!approval.session_id && !!approval.task_id && taskAction(approval.task_id, "Open task")}
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
    if ("pair" in item) {
      return (
        <>
          {sessionAction(item.pair.session_a_id, `Open ${item.pair.session_a}`)}
          {sessionAction(item.pair.session_b_id, `Open ${item.pair.session_b}`)}
        </>
      );
    }
    return taskAction(
      item.task.id,
      item.reason === "review-task" ? "Review" : "Open task",
    );
  }
}

