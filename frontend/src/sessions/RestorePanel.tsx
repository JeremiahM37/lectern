import React from "react";
import type { SessionsApi } from "./Sessions";
import { closedAge, type RestorableSession } from "./restore";

interface RestorePanelProps {
  api: SessionsApi;
  onNotice(message: string, error?: boolean): void;
  // onReopen runs the record's own action; onElsewhere opens the agent picker.
  onReopen(session: RestorableSession): Promise<void>;
  onElsewhere(session: RestorableSession): void;
  refreshVersion?: number;
}

// Restore is the one place to bring back anything that closed: sessions you
// ended, archived ones, ones that exited on their own, and ones a restart
// interrupted. Rows are grouped by project, newest first, and each shows what
// reopening it will do before you press it.
export function RestorePanel({
  api,
  onNotice,
  onReopen,
  onElsewhere,
  refreshVersion = 0,
}: RestorePanelProps) {
  const [rows, setRows] = React.useState<RestorableSession[]>(),
    [busy, setBusy] = React.useState(false),
    [query, setQuery] = React.useState(""),
    [actionID, setActionID] = React.useState<number>();

  async function load() {
    setBusy(true);
    try {
      setRows(
        await api.request<RestorableSession[]>("/sessions/restorable?limit=100"),
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

  const groups = React.useMemo(() => {
    const terms = query.toLocaleLowerCase().trim().split(/\s+/).filter(Boolean);
    const out = new Map<string, RestorableSession[]>();
    for (const row of rows || []) {
      const haystack = [
        row.name,
        row.project_name,
        row.agent,
        row.model,
        row.target_name,
        row.workdir,
        row.group_path,
        row.reason_label,
        row.preview,
      ]
        .join(" ")
        .toLocaleLowerCase();
      if (!terms.every((term) => haystack.includes(term))) continue;
      const key = row.project_name || "No project";
      out.set(key, [...(out.get(key) || []), row]);
    }
    return [...out.entries()];
  }, [rows, query]);

  return (
    <section className="recent-closed" aria-label="Restore sessions">
      <div className="recent-closed-head">
        <div>
          <h3>Restore</h3>
          <p>Closed, archived and interrupted sessions, newest first.</p>
        </div>
        {busy && <span className="hint">Loading…</span>}
      </div>
      <input
        id="restore-search"
        className="f"
        type="search"
        placeholder="Search names, projects, folders or the last message"
        aria-label="Search restorable sessions"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      {!rows ? (
        <p className="hint">Loading restorable sessions…</p>
      ) : rows.length === 0 ? (
        <p className="hint">Nothing to restore.</p>
      ) : groups.length === 0 ? (
        <p className="hint">No closed session matches your search.</p>
      ) : (
        groups.map(([project, items]) => (
          <div className="restore-group" key={project}>
            <h4>{project}</h4>
            <div className="recent-list">
              {items.map((session) => (
                <div
                  className="recent-row"
                  key={session.id}
                  data-session-id={session.id}
                >
                  <div className="recent-details">
                    <strong>{session.name || "Unnamed session"}</strong>
                    <span>
                      {session.reason_label} ·{" "}
                      {session.agent === "shell"
                        ? "shell"
                        : session.model
                          ? `${session.agent} · ${session.model}`
                          : session.agent || "unknown agent"}{" "}
                      · {closedAge(session.ended_at ?? session.updated_at)}
                    </span>
                    {session.preview && (
                      <q className="restore-preview" title={session.preview}>
                        {session.preview}
                      </q>
                    )}
                    <small className="restore-note">{session.note}</small>
                  </div>
                  <div className="restore-actions">
                    <button
                      className={session.action === "history" ? "b" : "b ok"}
                      disabled={busy || actionID === session.id}
                      onClick={() => {
                        setActionID(session.id);
                        void onReopen(session).finally(() =>
                          setActionID(undefined),
                        );
                      }}
                    >
                      {actionID === session.id ? "Restoring…" : session.action_label}
                    </button>
                    {session.agent !== "shell" && session.action !== "track" && (
                      <button
                        className="b restore-elsewhere"
                        disabled={busy || actionID === session.id}
                        onClick={() => onElsewhere(session)}
                      >
                        Other agent…
                      </button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>
        ))
      )}
    </section>
  );
}
