import { useEffect, useState } from "react";
import type { Claim, Project } from "../types";
import { Modal } from "../sessions/Modal";
import { t, useLocale } from "../i18n";
import "./claims.css";

// ClaimsPanel is the Claim board's human surface (docs/claims.md point 5): a
// shared, vendor-neutral record of who is doing what in a repository, so
// agents from different vendors — and a human — coordinate instead of
// duplicating work. Board-wide list, filter by project, release, extend,
// and "claim for me" (holder = the signed-in principal, resolved server-side
// from internal/auth).
export interface ClaimsApi {
  claims(
    filter?: { repo_key?: string; project_id?: number; session_id?: number; attempt_id?: number },
    signal?: AbortSignal,
  ): Promise<Claim[]>;
  createClaim(body: {
    repo_key?: string;
    project_id?: number;
    session_id?: number;
    attempt_id?: number;
    scope_kind: "task" | "paths" | "topic";
    scope?: string;
    paths?: string[];
    holder?: string;
    intent?: string;
    ttl_minutes?: number;
  }): Promise<Claim>;
  releaseClaim(id: number): Promise<{ released: boolean }>;
  extendClaim(id: number, ttlMinutes?: number): Promise<Claim>;
}

export function parseClaimPaths(claim: Claim): string[] {
  if (claim.scope_kind !== "paths") return [];
  try {
    const parsed: unknown = JSON.parse(claim.scope);
    if (Array.isArray(parsed)) return parsed.filter((v): v is string => typeof v === "string");
  } catch {
    /* fall through to the comma-separated fallback the backend also accepts */
  }
  return claim.scope
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

export function claimScopeLabel(claim: Claim): string {
  if (claim.scope_kind === "paths") return parseClaimPaths(claim).join(", ");
  if (claim.scope_kind === "task") return t("board.claims.taskScope", { id: claim.scope });
  return claim.scope;
}

function ago(seconds: number): string {
  const s = Date.now() / 1000 - seconds;
  if (s < 60) return t("board.claims.justNow");
  const m = Math.round(s / 60);
  if (m < 60) return t("board.claims.minutesAgo", { n: m });
  const h = Math.round(m / 60);
  return t("board.claims.hoursAgo", { n: h });
}

function inFuture(seconds: number): string {
  const s = seconds - Date.now() / 1000;
  if (s <= 0) return t("board.claims.expiring");
  const m = Math.round(s / 60);
  if (m < 60) return t("board.claims.inMinutes", { n: m });
  const h = Math.round(m / 60);
  return t("board.claims.inHours", { n: h });
}

export function ClaimsPanel({
  api,
  projects,
  onClose,
  onNotice,
  initialProjectId,
}: {
  api: ClaimsApi;
  projects: Project[];
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
  initialProjectId?: number;
}) {
  useLocale();
  const [claims, setClaims] = useState<Claim[]>([]);
  const [projectFilter, setProjectFilter] = useState<number | "">(initialProjectId ?? "");
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState<number | null>(null);
  const [showForm, setShowForm] = useState(false);
  const [formProject, setFormProject] = useState(initialProjectId ?? projects[0]?.id ?? 0);
  const [scopeKind, setScopeKind] = useState<"task" | "paths" | "topic">("topic");
  const [scope, setScope] = useState("");
  const [intent, setIntent] = useState("");
  const [ttl, setTtl] = useState(120);

  async function load() {
    setLoading(true);
    try {
      const rows = await api.claims(projectFilter === "" ? {} : { project_id: projectFilter });
      setClaims(rows);
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    const timer = window.setInterval(() => void load(), 15000);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectFilter]);

  async function release(id: number) {
    setBusy(id);
    try {
      await api.releaseClaim(id);
      setClaims((old) => old.filter((c) => c.id !== id));
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(null);
    }
  }

  async function extend(id: number) {
    setBusy(id);
    try {
      const updated = await api.extendClaim(id);
      setClaims((old) => old.map((c) => (c.id === id ? updated : c)));
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(null);
    }
  }

  async function submitClaimForMe(e: React.FormEvent) {
    e.preventDefault();
    if (!formProject || !scope.trim()) {
      onNotice(t("board.claims.needProjectScope"), true);
      return;
    }
    try {
      const body: Parameters<ClaimsApi["createClaim"]>[0] = {
        project_id: formProject,
        scope_kind: scopeKind,
        intent: intent.trim(),
        ttl_minutes: ttl,
      };
      if (scopeKind === "paths") {
        body.paths = scope.split(",").map((s) => s.trim()).filter(Boolean);
      } else {
        body.scope = scope.trim();
      }
      await api.createClaim(body);
      setScope("");
      setIntent("");
      setShowForm(false);
      void load();
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  return (
    <Modal id="claims-panel" open className="sheet claims-panel" aria-label={t("board.claims.title")} onCancel={onClose}>
      <header className="sheet-head">
        <h2>{t("board.claims.title")}</h2>
        <button onClick={onClose}>✕</button>
      </header>
      <p>
        {t("board.claims.introBefore")}{" "}
        <code>claim_work</code>{t("board.claims.introAfter")}
      </p>
      <div className="claims-toolbar">
        <select
          aria-label={t("board.claims.filterByProject")}
          value={projectFilter}
          onChange={(e) => setProjectFilter(e.target.value ? Number(e.target.value) : "")}
        >
          <option value="">{t("board.claims.allRepositories")}</option>
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
        </select>
        <button type="button" className="b" onClick={() => setShowForm((v) => !v)}>
          {showForm ? t("board.claims.cancel") : t("board.claims.claimForMe")}
        </button>
      </div>
      {showForm && (
        <form className="claims-form" onSubmit={submitClaimForMe}>
          <select
            aria-label={t("board.claims.projectToClaim")}
            value={formProject}
            onChange={(e) => setFormProject(Number(e.target.value))}
          >
            {projects.map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </select>
          <select
            aria-label={t("board.claims.scopeKind")}
            value={scopeKind}
            onChange={(e) => setScopeKind(e.target.value as "task" | "paths" | "topic")}
          >
            <option value="topic">{t("board.claims.kindTopicOption")}</option>
            <option value="paths">{t("board.claims.kindPathsOption")}</option>
            <option value="task">{t("board.claims.kindTaskOption")}</option>
          </select>
          <input
            aria-label={t("board.claims.scope")}
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            placeholder={
              scopeKind === "paths"
                ? "frontend/src/sessions/**, frontend/src/board/**"
                : scopeKind === "task"
                  ? "42"
                  : t("board.claims.topicPlaceholder")
            }
          />
          <input
            aria-label={t("board.claims.intent")}
            value={intent}
            onChange={(e) => setIntent(e.target.value)}
            placeholder={t("board.claims.intentPlaceholder")}
          />
          <input
            aria-label={t("board.claims.ttlMinutes")}
            type="number"
            min={5}
            max={1440}
            value={ttl}
            onChange={(e) => setTtl(Number(e.target.value))}
          />
          <button className="b ok" type="submit">
            {t("board.claims.claim")}
          </button>
        </form>
      )}
      {loading && claims.length === 0 && <p>{t("board.claims.loading")}</p>}
      {!loading && claims.length === 0 && <p>{t("board.claims.empty")}</p>}
      <ul className="claims-list">
        {claims.map((c) => (
          <li key={c.id} className="claims-row" data-id={c.id} data-scope-kind={c.scope_kind}>
            <div className="claims-row-main">
              <span className="claims-holder">
                {c.holder_kind === "session" && `#${c.session_id} `}
                {c.holder_kind === "attempt" && `#${c.attempt_id} `}
                {c.holder}
                {c.agent && <span className="claims-agent"> ({c.agent})</span>}
                {c.auto && <span className="claims-auto-badge" title={t("board.claims.autoTitle")}>{t("board.claims.auto")}</span>}
              </span>
              <span className="claims-scope">
                <b>{t(`board.claims.kind.${c.scope_kind}`, undefined, c.scope_kind)}</b> {claimScopeLabel(c)}
              </span>
              {c.intent && <span className="claims-intent">“{c.intent}”</span>}
              <span className="claims-times">
                {t("board.claims.times", { claimed: ago(c.created_at), expires: inFuture(c.expires_at) })}
              </span>
            </div>
            <div className="claims-row-actions">
              <button className="b" disabled={busy === c.id} onClick={() => void extend(c.id)}>
                {t("board.claims.extend")}
              </button>
              <button className="b" disabled={busy === c.id} onClick={() => void release(c.id)}>
                {t("board.claims.release")}
              </button>
            </div>
          </li>
        ))}
      </ul>
    </Modal>
  );
}
