// Graduated approval choices for a pending PermissionRequest, rendered
// through the same tool-view registry the chat cards use (a Bash approval
// shows the command, an Edit approval shows the diff) instead of
// `JSON.stringify(approval.input)`. Shared by Conversation.tsx's session
// approval block and NeedsYou.tsx so both surfaces offer the same choices.
import { useState } from "react";
import type { Approval } from "../types";
import { ToolCardBody, ToolCardHeader } from "./tool-views/ToolCard";
import type { ToolCard } from "./tool-views/chatCards";
import { t, useLocale } from "../i18n";

export interface ApprovalDecisionOptions {
  forSession?: boolean;
  note?: string;
}

type Busy = "once" | "session" | "deny" | null;

// Mirrors internal/policy BroadenableForSession: a remembered session rule
// matches a Bash command by its first word, so for a wrapper (sudo, bash -c,
// env, …) or a path/assignment it would allow far more than this one command.
const WRAPPERS = new Set([
  "sudo", "doas", "su", "env", "exec", "eval", "bash", "sh", "zsh", "dash", "fish",
  "xargs", "nohup", "timeout", "nice", "time", "command", "python", "python3", "node", "perl", "ruby",
]);

/** The label for "allow for this session", or null when that scope would be too broad. */
export function sessionScopeLabel(tool: string, input: Record<string, unknown>): string | null {
  if (tool !== "Bash") return t("conversation.approval.allowTool", { tool });
  const first = String(input.command ?? "").trim().split(/\s+/)[0] || "";
  if (!first || WRAPPERS.has(first) || /[/=$`(]/.test(first)) return null;
  return t("conversation.approval.allowCommand", { command: first });
}

export function ApprovalCard({
  approval,
  onDecide,
  onOpenTask,
}: {
  approval: Approval;
  onDecide(decision: "approved" | "denied", opts?: ApprovalDecisionOptions): Promise<void> | void;
  /** Only NeedsYou needs this — Conversation.tsx already has its own "open task" link and closes itself instead. */
  onOpenTask?(taskId: number): void;
}) {
  useLocale();
  const [busy, setBusy] = useState<Busy>(null);
  const [denying, setDenying] = useState(false);
  const [note, setNote] = useState("");

  const card: ToolCard = {
    kind: "tool",
    id: `approval-${approval.id}`,
    name: approval.tool_name,
    input: approval.input || {},
    status: "pending",
  };
  const decided = approval.status !== "pending";
  const sessionLabel = sessionScopeLabel(approval.tool_name, approval.input || {});

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

  return (
    <div className="approval-card" data-decided={decided ? approval.status : undefined}>
      <div className="approval-head">
        <strong>{t("conversation.approval.needed", { tool: approval.tool_name })}</strong>
        <ToolCardHeader card={card} />
      </div>
      <ToolCardBody card={card} />
      {!decided && (
        <div className="approval-actions">
          <button type="button" className="b ok" disabled={busy !== null} onClick={() => void act("once", "approved")}>
            {busy === "once" ? t("conversation.approval.allowing") : t("conversation.approval.allowOnce")}
          </button>
          {/* Only a session-scoped approval has a session to remember the
              rule against — a task attempt's approval has no persistent
              session, so there is nothing "for later this session" to be. */}
          {!!approval.session_id && sessionLabel && (
            <button
              type="button"
              className="b"
              disabled={busy !== null}
              onClick={() => void act("session", "approved", { forSession: true })}
            >
              {busy === "session" ? t("conversation.approval.allowing") : sessionLabel}
            </button>
          )}
          {!denying ? (
            <button type="button" className="b warn" disabled={busy !== null} onClick={() => setDenying(true)}>
              {t("conversation.approval.deny")}
            </button>
          ) : (
            <div className="approval-deny">
              <label htmlFor={`approval-note-${approval.id}`}>{t("conversation.approval.why")}</label>
              <textarea
                id={`approval-note-${approval.id}`}
                rows={2}
                value={note}
                placeholder={t("conversation.approval.notePlaceholder")}
                onChange={(e) => setNote(e.target.value)}
              />
              <div className="approval-deny-actions">
                <button type="button" className="b" disabled={busy !== null} onClick={() => setDenying(false)}>
                  {t("conversation.approval.cancel")}
                </button>
                <button
                  type="button"
                  className="b warn"
                  disabled={busy !== null}
                  onClick={() => void act("deny", "denied", { note: note.trim() || undefined })}
                >
                  {busy === "deny" ? t("conversation.approval.denying") : t("conversation.approval.denyWithFeedback")}
                </button>
              </div>
            </div>
          )}
        </div>
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
  );
}
