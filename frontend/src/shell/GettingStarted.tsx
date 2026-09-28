import { useEffect, useState } from "react";
import { t, useLocale } from "../i18n";

interface AgentCheck {
  name: string;
  found: boolean;
}
interface EnvCheck {
  ok: boolean;
  fix?: string;
}
interface OnboardingStatus {
  agents: AgentCheck[];
  tmux: EnvCheck;
  git: EnvCheck;
  python?: EnvCheck;
}

// The Sessions page's empty state, which is also the first screen after an
// install (docs/design/simple-ui.md "First run"): one sentence, one primary
// action, and — only when something is actually missing — the fix for it.
// Nothing here is a checklist to tick: a project is made by starting in a
// folder, and a check that passes is not worth a line.
export function GettingStarted({
  request,
  onStart,
  onDemo,
  onFind,
}: {
  request: <T>(path: string) => Promise<T>;
  onStart(): void;
  onDemo(): Promise<void>;
  onFind(): void;
}) {
  useLocale();
  const [status, setStatus] = useState<OnboardingStatus>();
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    request<OnboardingStatus>("/onboarding")
      .then((row) => live && setStatus(row))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [request]);
  const noAgent = !!status && !status.agents.some((agent) => agent.found);
  const problems: { key: string; text: string; fix?: string }[] = [];
  if (noAgent) problems.push({ key: "agent", text: t("start.problem.agent"), fix: t("start.problem.agentFix") });
  if (status && !status.tmux.ok) problems.push({ key: "tmux", text: t("start.problem.tmux"), fix: status.tmux.fix });
  if (status && !status.git.ok) problems.push({ key: "git", text: t("start.problem.git"), fix: status.git.fix });
  if (status?.python && !status.python.ok) problems.push({ key: "python", text: t("start.problem.python"), fix: status.python.fix });
  return (
    <section className="getting-started" id="getting-started" aria-labelledby="getting-started-title">
      <h2 id="getting-started-title">{t("start.title")}</h2>
      <p>{t("start.intro")}</p>
      <div className="gs-actions">
        <button type="button" className="b ok" id="gs-start" onClick={onStart}>
          {t("start.action")}
        </button>
        {noAgent && (
          <button
            type="button"
            className="b"
            id="gs-demo"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await onDemo();
              } finally {
                setBusy(false);
              }
            }}
          >
            {busy ? t("start.demoStarting") : t("start.demo")}
          </button>
        )}
        <button type="button" className="linkish" id="gs-find" onClick={onFind}>
          {t("start.find")}
        </button>
      </div>
      {problems.length > 0 && (
        <ul className="gs-problems" aria-label={t("start.problems")}>
          {problems.map((row) => (
            <li key={row.key} data-check={row.key}>
              {row.text}
              {row.fix && (
                <>
                  {" "}
                  <code>{row.fix}</code>
                </>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
