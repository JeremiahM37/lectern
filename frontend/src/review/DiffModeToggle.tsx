import type { DiffMode } from "./diffModel";
import { t, useLocale } from "../i18n";

/** Unified or side-by-side; the choice is a per-device preference (prefs.ts). */
export const DiffModeToggle = ({ mode, onChange }: { mode: DiffMode; onChange(m: DiffMode): void }) => {
  useLocale();
  return (
  <span className="seg" role="group" aria-label={t("review.mode.label")}>
    <button type="button" aria-pressed={mode === "unified"} onClick={() => onChange("unified")}>
      {t("review.mode.unified")}
    </button>
    <button type="button" aria-pressed={mode === "split"} onClick={() => onChange("split")}>
      {t("review.mode.split")}
    </button>
  </span>
  );
};
