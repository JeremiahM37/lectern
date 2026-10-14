// Pure helpers for the semantic-conflict chip. They mirror GET
// /api/conflicts and POST /api/conflicts/validate (internal/api/conflicts_api.go).
// Kept free of React and fetch so the decisions can be tested directly.

export interface ConflictRef {
  kind: "session" | "attempt";
  id: number;
  title: string;
}

export interface ConflictPair {
  a: ConflictRef;
  b: ConflictRef;
  level: "semantic" | "textual" | "none";
  reasons: string[];
}

export interface ValidateResult {
  applied: boolean;
  textual_conflict: boolean;
  check_status: "passed" | "failed" | "error" | "skipped";
  command?: string;
  output_tail: string;
  reason?: string;
}

// A risk worth showing is one the server calls semantic. Textual overlap is
// already covered by the file-overlap chip, so this chip does not repeat it.
export function semanticRiskFor(
  pairs: ConflictPair[],
  sessionId: number,
): { other: ConflictRef; reason: string } | null {
  for (const p of pairs) {
    if (p.level !== "semantic") continue;
    const onA = p.a.kind === "session" && p.a.id === sessionId;
    const onB = p.b.kind === "session" && p.b.id === sessionId;
    if (!onA && !onB) continue;
    return { other: onA ? p.b : p.a, reason: p.reasons[0] ?? "" };
  }
  return null;
}

// The request body for "Check together" from this card's side to the other.
export function validateBody(sessionId: number, other: ConflictRef): {
  a: { session_id: number };
  b: { session_id?: number; attempt_id?: number };
} {
  return {
    a: { session_id: sessionId },
    b: other.kind === "attempt" ? { attempt_id: other.id } : { session_id: other.id },
  };
}

// The outcome word the chip shows. A textual conflict wins over everything,
// because then the check never ran.
export function validationOutcome(r: ValidateResult): "textual" | "passed" | "failed" | "error" | "skipped" {
  if (r.textual_conflict) return "textual";
  return r.check_status;
}

export const CONFLICTS_CACHE_MS = 30_000;

// conflictsPath is the list endpoint for one project. A session without a
// project has no scan to read.
export function conflictsPath(projectId: number | null | undefined): string | null {
  if (!projectId || projectId <= 0) return null;
  return `/conflicts?project_id=${projectId}`;
}

// isFresh says whether a cached scan from `at` (ms) is still usable at `now`.
export function isFresh(at: number, now: number, ttlMs = CONFLICTS_CACHE_MS): boolean {
  return now - at < ttlMs;
}
