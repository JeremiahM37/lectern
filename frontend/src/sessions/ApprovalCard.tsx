// The one approval component (docs/design/simple-ui.md "One approval
// component"): the session card, the Approvals page, the chat sheet, the
// Needs-you list and the page a notification opens all render this, so a
// person sees the same three choices with the same words everywhere —
// Allow once · Allow for this session · Deny… — and the same keys, Y / A / N,
// while the card has focus. The tool call is shown through the tool-view
// registry the chat cards use (a Bash approval shows the command, an Edit
// approval shows the diff), or as one line when `compact`.
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { Approval } from "../types";
import { ToolCardBody, ToolCardHeader } from "./tool-views/ToolCard";
import type { ToolCard } from "./tool-views/chatCards";
import { approvalSummary } from "./approval-summary";
import { t, useLocale } from "../i18n";
import { useDictation } from "../voice";

export interface ApprovalDecisionOptions {
  forSession?: boolean;
  /** A task's approval: write a project rule so it is never asked again. */
  always?: boolean;
  note?: string;
}

type Busy = "once" | "second" | "deny" | null;

// Mirrors internal/policy BroadenableForSession: a remembered session rule
// matches a Bash command by its first word, so for a wrapper (sudo, bash -c,
// env, …) or a path/assignment it would allow far more than this one command.
const WRAPPERS = new Set([
  "sudo", "doas", "su", "env", "exec", "eval", "bash", "sh", "zsh", "dash", "fish",
  "xargs", "nohup", "timeout", "nice", "time", "command", "python", "python3", "node", "perl", "ruby",
]);

/**
 * What "Allow for this session" would also allow: `{kind: "command", word}`
 * for a Bash command (matched by its first word), `{kind: "tool"}` for any
 * other tool, or `{kind: "broad", word}` when a session rule would allow
 * almost anything, and so is not offered.
 */
export function sessionScope(tool: string, input: Record<string, unknown>): { kind: "command" | "tool" | "broad"; word: string } {
  if (tool !== "Bash") return { kind: "tool", word: tool };
  const first = String(input.command ?? "").trim().split(/\s+/)[0] || "";
  if (!first || WRAPPERS.has(first) || /[/=$`(]/.test(first)) return { kind: "broad", word: first };
  return { kind: "command", word: first };
}

export function ApprovalCard({
  approval,
  onDecide,
  onOpenTask,
  onOpenSession,
  compact = false,
  showContext = false,
  autoFocus = false,
  onNotice,
}: {
  approval: Approval;
  onDecide(decision: "approved" | "denied", opts?: ApprovalDecisionOptions): Promise<void> | void;
  onOpenTask?(taskId: number): void;
  onOpenSession?(sessionId: number): void;
  /** One line for the tool call instead of its full view (session cards, lists). */
  compact?: boolean;
  /** Name the session or task it came from (the Approvals page). */
  showContext?: boolean;
  autoFocus?: boolean;
  /** Given, the deny note can be dictated (it reports dictation errors). */
  onNotice?(text: string, error?: boolean): void;
}) {
  useLocale();
  const [busy, setBusy] = useState<Busy>(null);
  const [denying, setDenying] = useState(false);
  const [note, setNote] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const noteBox = useRef<HTMLTextAreaElement>(null);
  const dictation = useDictation({ onChange: setNote, onNotice: onNotice ?? (() => {}) });
  const canDictate = !!onNotice && dictation.supported;
  useEffect(() => {
    if (autoFocus) root.current?.focus({ preventScroll: true });
  }, [autoFocus]);
  useEffect(() => {
    if (denying) noteBox.current?.focus();
  }, [denying]);

  const card: ToolCard = {
    kind: "tool",
    id: `approval-${approval.id}`,
    name: approval.tool_name,
    input: approval.input || {},
    status: "pending",
  };
  const decided = approval.status !== "pending";
  const forSession = !!approval.session_id;
  const scope = sessionScope(approval.tool_name, approval.input || {});
  // The second choice: remember for the rest of this session, or — for a
  // task, which has no session — for this project.
  const secondLabel = forSession ? t("approval.allowSession") : t("approval.alwaysProject");
  const secondBlocked = forSession && scope.kind === "broad";
  const secondNote = !forSession
    ? t("approval.alwaysProjectNote")
    : scope.kind === "broad"
      ? t("approval.scopeTooBroad", { command: scope.word || approval.tool_name })
      : scope.kind === "command"
        ? t("approval.scopeCommand", { command: scope.word })
        : t("approval.scopeTool", { tool: scope.word });

  async function act(which: Busy, decision: "approved" | "denied", opts?: ApprovalDecisionOptions) {
    if (busy || decided) return;
    setBusy(which);
    try {
      await onDecide(decision, opts);
      setDenying(false);
      setNote("");
    } finally {
      setBusy(null);
    }
  }
  const allowOnce = () => void act("once", "approved");
  const allowSecond = () => {
    if (secondBlocked) return;
    void act("second", "approved", forSession ? { forSession: true } : { always: true });
  };
  const deny = () => void act("deny", "denied", { note: note.trim() || undefined });

  function keys(event: KeyboardEvent<HTMLDivElement>) {
    if (decided || busy || event.altKey || event.ctrlKey || event.metaKey) return;
    const tag = (event.target as HTMLElement).tagName;
    if (tag === "TEXTAREA" || tag === "INPUT" || tag === "SELECT") return;
    const key = event.key.toLowerCase();
    if (key === "y") allowOnce();
    else if (key === "a") allowSecond();
    else if (key === "n") setDenying(true);
    else return;
    event.preventDefault();
  }

  const context = approval.session_id
    ? approval.session_name || t("approval.sessionFallback", { id: approval.session_id })
    : approval.task_id
      ? t("approval.taskContext", { id: approval.task_id, title: approval.task_title || "" })
      : "";
  return (
    <div
      ref={root}
      className={`approval-card${compact ? " compact" : ""}`}
      data-decided={decided ? approval.status : undefined}
      data-approval-id={approval.id}
      tabIndex={decided ? undefined : 0}
      role="group"
      aria-label={t("approval.label", { tool: approval.tool_name })}
      aria-keyshortcuts={decided ? undefined : "y a n"}
      onKeyDown={keys}
    >
      <div className="approval-head">
        <strong>{t("approval.needed", { tool: approval.tool_name })}</strong>
        {showContext && context && <span className="approval-context">{context}</span>}
        {!compact && <ToolCardHeader card={card} />}
      </div>
      {compact ? (
        approvalSummary(approval) && <code className="approval-summary">{approvalSummary(approval)}</code>
      ) : (
        <ToolCardBody card={card} />
      )}
      {!decided && !denying && (
        <>
          <div className="approval-actions">
            <button type="button" className="b ok" disabled={busy !== null} aria-keyshortcuts="y" onClick={allowOnce}>
              {busy === "once" ? t("conversation.approval.allowing") : t("conversation.approval.allowOnce")}
              <kbd className="approval-kbd" aria-hidden="true">Y</kbd>
            </button>
            <button
              type="button"
              className="b approval-second"
              disabled={busy !== null || secondBlocked}
              aria-keyshortcuts={secondBlocked ? undefined : "a"}
              aria-describedby={`approval-scope-${approval.id}`}
              title={secondNote}
              onClick={allowSecond}
            >
              {busy === "second" ? t("conversation.approval.allowing") : secondLabel}
              {!secondBlocked && <kbd className="approval-kbd" aria-hidden="true">A</kbd>}
            </button>
            <button type="button" className="b warn" disabled={busy !== null} aria-keyshortcuts="n" onClick={() => setDenying(true)}>
              {t("conversation.approval.deny")}
              <kbd className="approval-kbd" aria-hidden="true">N</kbd>
            </button>
          </div>
          <p className="approval-scope subhint" id={`approval-scope-${approval.id}`}>
            {secondNote}
          </p>
        </>
      )}
      {!decided && denying && (
        <form
          className="approval-deny"
          onSubmit={(event) => {
            event.preventDefault();
            deny();
          }}
        >
          <label htmlFor={`approval-note-${approval.id}`}>{t("conversation.approval.why")}</label>
          <textarea
            ref={noteBox}
            id={`approval-note-${approval.id}`}
            rows={2}
            value={note}
            placeholder={t("conversation.approval.notePlaceholder")}
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                setDenying(false);
                root.current?.focus();
              } else if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                deny();
              }
            }}
          />
          <div className="approval-deny-actions">
            {canDictate && (
              <button
                type="button"
                className={dictation.dictating ? "b mic-recording" : "b"}
                id={`approval-mic-${approval.id}`}
                aria-label={dictation.dictating ? t("sessions.needsYou.stopDictating") : t("sessions.needsYou.dictate")}
                aria-pressed={dictation.dictating}
                onClick={() => dictation.toggle(note)}
              >
                {dictation.transcribing ? "⏳" : dictation.dictating ? "🔴" : "🎙"}
              </button>
            )}
            <button type="button" className="b" disabled={busy !== null} onClick={() => setDenying(false)}>
              {t("conversation.approval.cancel")}
            </button>
            <button type="submit" className="b warn" disabled={busy !== null}>
              {busy === "deny" ? t("conversation.approval.denying") : t("approval.denySubmit")}
            </button>
          </div>
        </form>
      )}
      {(onOpenSession && approval.session_id) || approval.task_id ? (
        <div className="approval-links">
          {onOpenSession && !!approval.session_id && (
            <button type="button" className="b link-button" onClick={() => onOpenSession(approval.session_id!)}>
              {t("approval.openSession")}
            </button>
          )}
          {!!approval.task_id &&
            (onOpenTask ? (
              <button type="button" className="b link-button" onClick={() => onOpenTask(approval.task_id!)}>
                {t("conversation.approval.openTask")}
              </button>
            ) : (
              <a className="b link-button" href={`#task/${approval.task_id}`}>
                {t("conversation.approval.openTask")}
              </a>
            ))}
        </div>
      ) : null}
    </div>
  );
}

/** The decision request every surface sends. */
export function decisionBody(decision: "approved" | "denied", opts: ApprovalDecisionOptions = {}) {
  return {
    decision,
    ...(opts.note ? { note: opts.note } : {}),
    ...(opts.forSession ? { for_session: true } : {}),
    ...(opts.always ? { always_allow: true } : {}),
  };
}
