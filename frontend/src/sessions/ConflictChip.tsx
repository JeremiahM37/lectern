import { useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import type { SessionView } from "../types";
import type { SessionsApi } from "./Sessions";
import {
  conflictsPath,
  isFresh,
  semanticRiskFor,
  validateBody,
  validationOutcome,
  type ConflictPair,
  type ValidateResult,
} from "./conflictModel";

// Scans are shared by every card of one project, so the list is fetched once
// per project per cache window instead of once per card.
const cache = new Map<string, { at: number; pairs: ConflictPair[] }>();

async function loadPairs(api: SessionsApi, path: string): Promise<ConflictPair[]> {
  const hit = cache.get(path);
  if (hit && isFresh(hit.at, Date.now())) return hit.pairs;
  const pairs = await api.request<ConflictPair[]>(path);
  cache.set(path, { at: Date.now(), pairs });
  return pairs;
}

// ConflictChip is the semantic-risk chip on a session card (docs/conflicts.md):
// another live session changes a symbol this one uses, or changes the same
// symbol. It is advisory. "Check together" asks the server to apply both diffs
// to a scratch worktree and run the project's check.
export function ConflictChip({ session: s, api }: { session: SessionView; api: SessionsApi }) {
  useLocale();
  const [pairs, setPairs] = useState<ConflictPair[]>([]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ValidateResult | null>(null);
  const [error, setError] = useState("");

  const path = conflictsPath(s.project_id);
  useEffect(() => {
    if (!path) return;
    let live = true;
    loadPairs(api, path)
      .then((p) => live && setPairs(p))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [api, path]);

  const risk = semanticRiskFor(pairs, s.id);
  if (!risk) return null;

  const check = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await api.request<ValidateResult>("/conflicts/validate", {
        method: "POST",
        body: validateBody(s.id, risk.other),
      });
      setResult(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  const outcome = result ? validationOutcome(result) : null;
  return (
    <span className="conflict-chip">
      <button
        type="button"
        className="chip warn conflict-chip-button"
        aria-expanded={open}
        title={risk.reason}
        onClick={() => setOpen((v) => !v)}
      >
        {t("sessions.conflict.chip", { id: risk.other.id })}
      </button>
      {open && (
        <div className="conflict-chip-detail">
          <div>{t("sessions.conflict.title", { reason: risk.reason })}</div>
          <button type="button" className="conflict-check" disabled={busy} onClick={check}>
            {busy ? t("sessions.conflict.checking") : t("sessions.conflict.check")}
          </button>
          {outcome && <div className={`conflict-result conflict-${outcome}`}>{t(`sessions.conflict.${outcome}`)}</div>}
          {result?.output_tail && <pre className="conflict-output">{result.output_tail}</pre>}
          {error && <div className="conflict-error">{t("sessions.conflict.requestFailed", { error })}</div>}
        </div>
      )}
    </span>
  );
}
