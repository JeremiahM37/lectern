import { useState } from "react";
import { t, useLocale } from "../i18n";
import type { EffectStatus, SessionView } from "../types";
import { effectClaims, summarizeEffect } from "./effect-summary";
import "./effect-badge.css";

// VERDICT_KEY maps a claim's verdict to its label. Unknown is shown as
// "unchecked": the probe could not decide, which is not the same as wrong.
const VERDICT_KEY: Record<EffectStatus, string> = {
  verified: "sessions.effect.verdict.verified",
  contradicted: "sessions.effect.verdict.contradicted",
  unknown: "sessions.effect.verdict.unknown",
};

// EffectBadge is the completion-claim check for one session's last turn
// (internal/effects, docs/effect-checks.md): a green chip when the claims held,
// a red one when a claim did not, grey when nothing could be decided. Tapping
// it opens the claims with their verdicts and evidence. It renders nothing when
// there is nothing to show, so sessions with no claims look as they did before.
export function EffectBadge({ session: s }: { session: SessionView }) {
  useLocale();
  const [open, setOpen] = useState(false);
  const effect = s.last_effect;
  const summary = summarizeEffect(effect);
  if (!effect || summary.tone === "none") return null;

  const label =
    summary.tone === "contradicted"
      ? t("sessions.effect.contradicted", { count: summary.contradicted })
      : summary.tone === "verified"
        ? t("sessions.effect.verified", { count: summary.verified })
        : t("sessions.effect.unknown", { count: summary.unknown });

  return (
    <span className="effect-badge">
      <button
        type="button"
        className={`chip effect-chip effect-${summary.tone}`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {label}
      </button>
      {open && (
        <div className="effect-popover" role="region" aria-label={t("sessions.effect.title")}>
          <ul>
            {effectClaims(effect).map((c, i) => (
              <li key={`${c.kind}-${i}`} data-status={c.status}>
                <span className="effect-verdict">{t(VERDICT_KEY[c.status] || VERDICT_KEY.unknown)}</span>{" "}
                <q>{c.sentence}</q>
                {c.evidence && <div className="effect-evidence">{c.evidence}</div>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </span>
  );
}
