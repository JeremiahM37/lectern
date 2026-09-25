import { useEffect, useState } from "react";
import type { Claim, SessionView } from "../types";
import { claimScopeLabel } from "./ClaimsPanel";
import "./claims.css";

// SessionClaims is the Claim board's per-session view (docs/claims.md point
// 5): every active claim in THIS session's repository — its own (with a
// release button) and every peer's (read-only, same as the awareness
// briefing this session's agent already sees). Uses the generic `request`
// every session-facing api object already carries, so it needs no wider api
// type than SessionsApi.
export function SessionClaims({
  session,
  request,
  onNotice,
}: {
  session: SessionView;
  request<T>(path: string, options?: { method?: string; body?: unknown }): Promise<T>;
  onNotice(text: string, error?: boolean): void;
}) {
  const [claims, setClaims] = useState<Claim[]>([]);
  const [expanded, setExpanded] = useState(false);
  const [busy, setBusy] = useState<number | null>(null);
  const repoKey = session.repo_key;

  useEffect(() => {
    if (!repoKey) {
      setClaims([]);
      return;
    }
    let cancelled = false;
    const load = () =>
      request<Claim[]>(`/claims?repo_key=${encodeURIComponent(repoKey)}`)
        .then((rows) => {
          if (!cancelled) setClaims(rows);
        })
        .catch(() => {
          /* best-effort, like the awareness overlap chip this sits beside */
        });
    void load();
    const timer = window.setInterval(load, 20000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [repoKey, request]);

  async function release(id: number) {
    setBusy(id);
    try {
      await request(`/claims/${id}`, { method: "DELETE" });
      setClaims((old) => old.filter((c) => c.id !== id));
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(null);
    }
  }

  if (!repoKey || claims.length === 0) return null;
  return (
    <span className="session-claims-chip">
      <button
        type="button"
        className="chip"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
        title="Active claims in this repository"
      >
        📌 {claims.length} claim{claims.length === 1 ? "" : "s"}
      </button>
      {expanded && (
        <div className="session-claims">
          {claims.map((c) => (
            <div key={c.id} className="session-claims-row">
              <span>
                {c.holder_kind === "session" && `#${c.session_id} `}
                {c.holder}
              </span>
              <span className="claims-scope">
                {c.scope_kind} {claimScopeLabel(c)}
              </span>
              {c.intent && <em>“{c.intent}”</em>}
              {c.session_id === session.id && (
                <button
                  type="button"
                  className="b"
                  disabled={busy === c.id}
                  onClick={() => void release(c.id)}
                >
                  Release
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </span>
  );
}
