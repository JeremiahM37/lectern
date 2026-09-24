import { useState } from "react";
import type { SessionCheck, SessionView } from "../types";
import type { SessionsApi } from "./Sessions";

// STATUS_LABEL/STATUS_ICON cover every status_json.status internal/checks
// writes (see internal/store/schema.go's session_checks table).
const STATUS_ICON: Record<string, string> = {
  running: "⏳",
  passed: "✓",
  failed: "✗",
  error: "⚠",
  skipped: "–",
};

// CheckBadge is the project verify_cmd/auto-detected .verify.yaml result for
// one session: a compact chip (passed/failed/running), tap-to-expand output,
// and a "Run check" button. Used on the session card and in the Conversation
// header — both pass the same SessionView, so the two stay in sync from a
// single onRefresh.
export function CheckBadge({
  session: s,
  api,
  onNotice,
}: {
  session: SessionView;
  api: SessionsApi;
  onNotice: (text: string, error?: boolean) => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [detail, setDetail] = useState<SessionCheck | null>(null);
  const [busy, setBusy] = useState(false);
  const lc = s.last_check;

  async function runNow() {
    setBusy(true);
    try {
      await api.request(`/sessions/${s.id}/checks`, { method: "POST" });
      onNotice("Check started");
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }

  async function toggle() {
    if (expanded) {
      setExpanded(false);
      return;
    }
    setExpanded(true);
    try {
      const rows = await api.request<SessionCheck[]>(
        `/sessions/${s.id}/checks?limit=1`,
      );
      setDetail(rows[0] || null);
    } catch {
      // the summary chip is still useful even if the output fetch fails
    }
  }

  return (
    <span className="check-badge">
      {lc && (
        <button
          type="button"
          className={`chip check-chip check-${lc.status}`}
          title={lc.command}
          aria-expanded={expanded}
          onClick={() => void toggle()}
        >
          {STATUS_ICON[lc.status] || "?"}{" "}
          {lc.status === "running"
            ? "checking…"
            : lc.status === "passed"
              ? "check passed"
              : lc.status === "failed"
                ? "check failed"
                : lc.status === "error"
                  ? "check error"
                  : "check skipped"}
        </button>
      )}
      <button
        type="button"
        className="chip check-run"
        disabled={busy}
        onClick={() => void runNow()}
      >
        {busy ? "Starting…" : "Run check"}
      </button>
      {expanded && (
        <div className="check-detail">
          <code>{detail?.command || lc?.command}</code>
          <pre>{detail?.output_tail || "(no output captured)"}</pre>
        </div>
      )}
    </span>
  );
}
