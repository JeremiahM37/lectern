import React from "react";
import type { SessionView } from "../types";
import type { SessionsApi } from "./Sessions";

export interface RecentSession extends SessionView {
  released: boolean;
  can_resume_recent: boolean;
  resume_recent_url?: string;
  conversations_url: string;
}

interface RecentlyClosedProps {
  api: SessionsApi;
  onNotice(message: string, error?: boolean): void;
  onRestore(session: RecentSession): Promise<void>;
  onResume(session: RecentSession): Promise<void>;
  onHistory(session: RecentSession): void;
  refreshVersion?: number;
}

function age(seconds: number | null): string {
  if (seconds == null) return "closed recently";
  const elapsed = Math.max(0, Date.now() / 1000 - seconds);
  if (elapsed < 60) return "closed just now";
  if (elapsed < 3600) return `closed ${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400)
    return `closed ${Math.floor(elapsed / 3600)}h ${Math.floor(elapsed / 60) % 60}m ago`;
  return `closed ${Math.floor(elapsed / 86400)}d ${Math.floor(elapsed / 3600) % 24}h ago`;
}

export function RecentlyClosed({
  api,
  onNotice,
  onRestore,
  onResume,
  onHistory,
  refreshVersion = 0,
}: RecentlyClosedProps) {
  const [rows, setRows] = React.useState<RecentSession[]>(),
    [busy, setBusy] = React.useState(false),
    [actionID, setActionID] = React.useState<number>();

  async function load() {
    setBusy(true);
    try {
      setRows(
        await api.request<RecentSession[]>("/sessions/recent?limit=30"),
      );
    } catch (error) {
      setRows([]);
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }

  React.useEffect(() => {
    void load();
  }, [refreshVersion]);

  return (
    <section className="recent-closed" aria-label="Recently closed sessions">
      <div className="recent-closed-head">
        <div>
          <h3>Recently closed</h3>
      <p>Your last 30 closed sessions.</p>
        </div>
        {busy && <span className="hint">Loading…</span>}
      </div>
      {!rows ? (
        <p className="hint">Loading recently closed sessions…</p>
      ) : rows.length === 0 ? (
        <p className="hint">No recently closed sessions.</p>
      ) : (
        <div className="recent-list">
          {rows.map((session) => {
            const restore = session.released && session.can_restore;
            return (
              <div className="recent-row" key={session.id}>
                <div className="recent-details">
                  <strong>{session.name || "Unnamed session"}</strong>
                  <span>
                    {session.project_name || "Unassigned"} · {session.agent || "unknown agent"} · {age(session.ended_at)}
                  </span>
                </div>
                <button
                  className={restore || session.can_resume_recent ? "b ok" : "b"}
                  disabled={busy || actionID === session.id}
                  onClick={() => {
                    if (!restore && !session.can_resume_recent) {
                      onHistory(session);
                      return;
                    }
                    setActionID(session.id);
                    void (restore ? onRestore(session) : onResume(session)).finally(
                      () => setActionID(undefined),
                    );
                  }}
                >
                  {restore
                    ? "Restore tracking"
                    : session.can_resume_recent
                      ? "Resume"
                      : "Choose history"}
                </button>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}
