import type { DiffMode } from "./diffModel";

/** Unified or side-by-side; the choice is a per-device preference (prefs.ts). */
export const DiffModeToggle = ({ mode, onChange }: { mode: DiffMode; onChange(m: DiffMode): void }) => (
  <span className="seg" role="group" aria-label="Diff layout">
    <button type="button" aria-pressed={mode === "unified"} onClick={() => onChange("unified")}>
      Unified
    </button>
    <button type="button" aria-pressed={mode === "split"} onClick={() => onChange("split")}>
      Side by side
    </button>
  </span>
);
