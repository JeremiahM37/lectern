// A pending approval for this terminal's session, shown as a strip above the
// terminal (round-3 audit B5): an agent that stops at "Bash(…)" in the pane is
// waiting for a person, and the terminal is where that person is looking. It
// is the same approval card every other surface renders — Allow once · Allow
// for this session · Deny… — and Y / A / N work while focus is anywhere but
// the terminal's own input.
import { useCallback, useEffect, useRef, useState } from "react";
import type { Approval } from "../types";
import { ApprovalCard, decisionBody, type ApprovalDecisionOptions } from "../sessions/ApprovalCard";
import { t, useLocale } from "../i18n";
import { json, request, errorMessage } from "./model";

const POLL_MS = 2000;

/** The approvals waiting on one session, oldest first. */
export function pendingFor(rows: Approval[], sessionId: number): Approval[] {
  return rows
    .filter((row) => row.status === "pending" && row.session_id === sessionId)
    .sort((a, b) => a.id - b.id);
}

export function ApprovalStrip({
  sessionId,
  onNotice,
  onDecided,
}: {
  sessionId: number;
  onNotice(text: string): void;
  /** After a decision: hand the keyboard back to the terminal. */
  onDecided?(): void;
}) {
  useLocale();
  const [rows, setRows] = useState<Approval[]>([]);
  const alive = useRef(true);
  const load = useCallback(async () => {
    try {
      const all = await json<Approval[]>("/api/approvals?status=pending");
      if (alive.current) setRows(pendingFor(Array.isArray(all) ? all : [], sessionId));
    } catch {
      // A missed poll keeps what is shown; the next one tries again.
    }
  }, [sessionId]);
  useEffect(() => {
    alive.current = true;
    void load();
    const tick = window.setInterval(() => {
      if (document.visibilityState === "visible") void load();
    }, POLL_MS);
    const wake = () => {
      if (document.visibilityState === "visible") void load();
    };
    document.addEventListener("visibilitychange", wake);
    window.addEventListener("focus", wake);
    return () => {
      alive.current = false;
      clearInterval(tick);
      document.removeEventListener("visibilitychange", wake);
      window.removeEventListener("focus", wake);
    };
  }, [load]);

  const first = rows[0];
  if (!first) return null;
  async function decide(decision: "approved" | "denied", opts?: ApprovalDecisionOptions) {
    try {
      await request(`/api/approvals/${first!.id}/decision`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(decisionBody(decision, opts)),
      });
      setRows((old) => old.filter((row) => row.id !== first!.id));
      onNotice(decision === "approved" ? t("app.approvals.approved") : t("app.approvals.denied"));
      onDecided?.();
      void load();
    } catch (error) {
      onNotice(errorMessage(error));
      void load();
    }
  }
  return (
    <section id="terminal-approval" className="terminal-approval-strip" aria-label={t("terminalPage.approval.label")}>
      <ApprovalCard key={first.id} compact globalKeys approval={first} onDecide={decide} />
      {rows.length > 1 && <p className="terminal-approval-more">{t("terminalPage.approval.more", { n: rows.length - 1 })}</p>}
    </section>
  );
}
