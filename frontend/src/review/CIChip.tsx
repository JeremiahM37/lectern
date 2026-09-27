import type { CIWatchView } from "../types";
import { t, useLocale } from "../i18n";

// Tone per ci_watches.state (internal/ciloop): reuses the board's chip
// colours — info while CI runs, warn while the agent is fixing, green on
// pass, red when the loop gave up or stopped.
const TONE: Record<string, string> = {
  pending: "info",
  failing: "warn",
  passed: "ds",
  capped: "bad",
  error: "bad",
  stalled: "bad",
};

// CIChip is the CI loop's status on a task or session card ("CI failing —
// attempt 2/3", "CI passed"); it links to the pull request. See
// docs/ci-loop.md.
export function CIChip({ ci }: { ci?: CIWatchView | null }) {
  useLocale();
  if (!ci) return null;
  const title = [ci.pr_url, ci.failing.length ? t("review.ci.failing", { checks: ci.failing.join(", ") }) : "", ci.detail || ""]
    .filter(Boolean)
    .join("\n");
  return (
    <a
      className={`chip ci-chip ci-${ci.state} ${TONE[ci.state] || ""}`}
      href={ci.pr_url}
      target="_blank"
      rel="noreferrer"
      title={title}
      onClick={(e) => e.stopPropagation()}
    >
      {ci.label}
    </a>
  );
}
