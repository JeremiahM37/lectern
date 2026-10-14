// Pure helpers for the effect badge (EffectBadge.tsx). They take the shape the
// server sends and return what the badge needs, with no i18n and no DOM, so the
// rules can be tested directly.
import type { EffectCheck, EffectResult } from "../types";

// EffectTone is the badge colour. contradicted outranks everything: one claim
// that did not hold is what a person needs to see. unknown is grey, and none
// means there is nothing to show.
export type EffectTone = "contradicted" | "verified" | "unknown" | "none";

export interface EffectSummary {
  tone: EffectTone;
  verified: number;
  contradicted: number;
  unknown: number;
}

export function summarizeEffect(effect: EffectCheck | null | undefined): EffectSummary {
  const verified = effect?.verified ?? 0;
  const contradicted = effect?.contradicted ?? 0;
  const unknown = effect?.unknown ?? 0;
  let tone: EffectTone = "none";
  if (contradicted > 0) tone = "contradicted";
  else if (verified > 0) tone = "verified";
  else if (unknown > 0) tone = "unknown";
  return { tone, verified, contradicted, unknown };
}

// effectClaims returns the per-claim results, tolerating a missing or malformed
// list so one bad row cannot break the session card.
export function effectClaims(effect: EffectCheck | null | undefined): EffectResult[] {
  const list = effect?.results;
  return Array.isArray(list) ? list.filter((r) => r && typeof r.sentence === "string") : [];
}
