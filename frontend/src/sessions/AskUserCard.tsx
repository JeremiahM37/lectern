// The card for a Scribe question or plan review (an AskUser approval). It
// shows what the agent asks, the options as buttons, a free-text box when the
// agent allows one, and for a plan review the plan with Execute, Clear
// context and execute, Revise (with feedback) and Cancel. The answer goes out
// through the same onDecide every approval uses, as "approved" with the answer
// as its note; Lectern holds the agent's hook until then.
import { useState } from "react";
import type { Approval } from "../types";
import { t, useLocale } from "../i18n";
import type { ApprovalDecisionOptions } from "./ApprovalCard";
import { PLAN_ANSWER, askUserView, reviseAnswer } from "./ask-user";

export function AskUserCard({
  approval,
  onDecide,
  onOpenSession,
  compact = false,
  showContext = false,
}: {
  approval: Approval;
  onDecide(decision: "approved" | "denied", opts?: ApprovalDecisionOptions): Promise<void> | void;
  onOpenSession?(sessionId: number): void;
  compact?: boolean;
  showContext?: boolean;
}) {
  useLocale();
  const view = askUserView(approval.input || {});
  const plan = view.kind === "plan_review";
  const decided = approval.status !== "pending";
  const [busy, setBusy] = useState<string | null>(null);
  const [revising, setRevising] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [freeText, setFreeText] = useState("");

  async function answer(which: string, text: string) {
    if (busy || decided || !text.trim()) return;
    setBusy(which);
    try {
      await onDecide("approved", { note: text.trim() });
    } finally {
      setBusy(null);
    }
  }

  const context = approval.session_id ? approval.session_name || t("approval.sessionFallback", { id: approval.session_id }) : "";
  const heading = plan ? t("askUser.planHeading") : t("askUser.questionHeading");

  return (
    <div
      className={`ask-user${compact ? " compact" : ""}`}
      data-decided={decided ? approval.status : undefined}
      data-approval-id={approval.id}
      data-ask-kind={view.kind}
      role="group"
      aria-label={t("askUser.label")}
    >
      <div className="ask-user-head">
        <strong>{heading}</strong>
        {showContext && context && <span className="approval-context">{context}</span>}
      </div>
      <p className="ask-user-question">{view.question}</p>
      {plan && view.body ? (
        <details open={!compact}>
          <summary>{t("askUser.showPlan")}</summary>
          <pre className="ask-user-body">{view.body}</pre>
        </details>
      ) : (
        !compact && view.body && <pre className="ask-user-body">{view.body}</pre>
      )}

      {decided ? (
        approval.status === "approved" && approval.note ? (
          <p className="ask-user-answered subhint">{t("askUser.answered", { answer: approval.note })}</p>
        ) : null
      ) : plan ? (
        <>
          <div className="ask-user-actions">
            <button type="button" className="b ok" disabled={busy !== null} onClick={() => void answer("execute", PLAN_ANSWER.execute)}>
              {busy === "execute" ? t("conversation.approval.allowing") : t("askUser.execute")}
            </button>
            <button type="button" className="b ok" disabled={busy !== null} onClick={() => void answer("clear", PLAN_ANSWER.clear)}>
              {busy === "clear" ? t("conversation.approval.allowing") : t("askUser.clearExecute")}
            </button>
            <button type="button" className="b" disabled={busy !== null} aria-expanded={revising} onClick={() => setRevising((v) => !v)}>
              {t("askUser.revise")}
            </button>
            <button type="button" className="b warn" disabled={busy !== null} onClick={() => void answer("cancel", PLAN_ANSWER.cancel)}>
              {t("askUser.cancel")}
            </button>
          </div>
          {revising && (
            <form
              className="ask-user-form"
              onSubmit={(event) => {
                event.preventDefault();
                void answer("revise", reviseAnswer(feedback));
              }}
            >
              <label htmlFor={`ask-revise-${approval.id}`}>{t("askUser.reviseLabel")}</label>
              <textarea
                id={`ask-revise-${approval.id}`}
                rows={3}
                value={feedback}
                onChange={(e) => setFeedback(e.target.value)}
              />
              <div className="ask-user-form-actions">
                <button type="submit" className="b ok" disabled={busy !== null}>
                  {busy === "revise" ? t("conversation.approval.allowing") : t("askUser.reviseSend")}
                </button>
              </div>
            </form>
          )}
        </>
      ) : (
        <>
          {view.options.length > 0 && (
            <div className="ask-user-actions">
              {view.options.map((option) => (
                <button key={option} type="button" className="b ok" disabled={busy !== null} onClick={() => void answer(option, option)}>
                  {busy === option ? t("conversation.approval.allowing") : option}
                </button>
              ))}
            </div>
          )}
          {view.allowFreeText && (
            <form
              className="ask-user-form"
              onSubmit={(event) => {
                event.preventDefault();
                void answer("text", freeText);
              }}
            >
              <label htmlFor={`ask-text-${approval.id}`}>{t("askUser.freeTextLabel")}</label>
              <textarea
                id={`ask-text-${approval.id}`}
                rows={2}
                value={freeText}
                placeholder={t("askUser.freeTextPlaceholder")}
                onChange={(e) => setFreeText(e.target.value)}
              />
              <div className="ask-user-form-actions">
                <button type="submit" className="b ok" disabled={busy !== null || !freeText.trim()}>
                  {busy === "text" ? t("conversation.approval.allowing") : t("askUser.send")}
                </button>
              </div>
            </form>
          )}
        </>
      )}
      {onOpenSession && !!approval.session_id && (
        <div className="approval-links">
          <button type="button" className="b link-button" onClick={() => onOpenSession(approval.session_id!)}>
            {t("approval.openSession")}
          </button>
        </div>
      )}
    </div>
  );
}
