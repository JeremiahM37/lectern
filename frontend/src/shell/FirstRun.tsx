import { useEffect, useState } from "react";
import { t, useLocale } from "../i18n";

interface AgentCheck {
  name: string;
  found: boolean;
  builtin: boolean;
}
interface EnvCheck {
  name: string;
  ok: boolean;
  detail?: string;
  fix?: string;
}
interface OnboardingStatus {
  agents: AgentCheck[];
  tmux: EnvCheck;
  git: EnvCheck;
  python?: EnvCheck;
}

export interface FirstRunProps {
  request: <T>(path: string) => Promise<T>;
  hasProject: boolean;
  hasSession: boolean;
  hasTarget: boolean;
  onSetupTarget: () => void;
  onStartSession: () => void;
  // Opens Settings, where the full "Connect your AI tools" card lives (see
  // ConnectTools.tsx). Optional so the standalone first-run harness/tests
  // that predate this link keep working with no prop supplied.
  onOpenConnectTools?: () => void;
}

type Item = { label: string; ok: boolean; detail?: string };

// FirstRun is the checklist a brand-new install shows instead of an empty
// kanban board full of column headers and nothing else. It appears only
// while there is genuinely nothing to look at (no project, no session) —
// the moment either exists, the ordinary board/sessions views take over and
// this never shows again. Every row that can be wrong carries its own fix;
// the one thing it asks you to do is the same button, always available.
export function FirstRun({ request, hasProject, hasSession, hasTarget, onSetupTarget, onStartSession, onOpenConnectTools }: FirstRunProps) {
  useLocale();
  const [status, setStatus] = useState<OnboardingStatus>();
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    request<OnboardingStatus>("/onboarding")
      .then((s) => {
        if (!cancelled) setStatus(s);
      })
      .catch((e) => {
        if (!cancelled) setError(String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [request]);

  const agentsFound = (status?.agents || []).filter((a) => a.found);
  const agentsOK = agentsFound.length > 0;
  const items: Item[] = [
    { label: t("app.firstRun.machine"), ok: hasTarget, detail: hasTarget ? undefined : t("app.firstRun.machineDetail") },
    {
      label: t("app.firstRun.agentCli"),
      ok: agentsOK,
      detail: agentsOK
        ? agentsFound.map((a) => a.name).join(", ")
        : t("app.firstRun.agentCliDetail"),
    },
    {
      label: t("app.firstRun.tmux"),
      ok: !!status?.tmux.ok,
      detail: status?.tmux.ok ? status.tmux.detail : status?.tmux.fix,
    },
    {
      label: t("app.firstRun.git"),
      ok: !!status?.git.ok,
      detail: status?.git.ok ? status.git.detail : status?.git.fix,
    },
    { label: t("app.firstRun.python"), ok: !!status?.python?.ok, detail: status?.python?.ok ? status.python.detail : status?.python?.fix },
    { label: t("app.firstRun.project"), ok: hasProject, detail: hasProject ? undefined : t("app.firstRun.projectDetail") },
    { label: t("app.firstRun.firstSession"), ok: hasSession },
  ];
  const loading = !status && !error;

  return (
    <section className="first-run" aria-labelledby="first-run-heading">
      <h2 id="first-run-heading">{t("app.firstRun.title")}</h2>
      <p className="first-run-sub">
        {t("app.firstRun.intro")}
      </p>
      {error && (
        <p className="first-run-error">
          {t("app.firstRun.statusError", { error })}
        </p>
      )}
      <ul className="first-run-checklist" aria-busy={loading}>
        {items.map((item) => (
          <li key={item.label} className={item.ok ? "ok" : "pending"}>
            <span className="first-run-mark" aria-hidden="true">
              {loading ? "…" : item.ok ? "✓" : "○"}
            </span>
            <span className="first-run-label">{item.label}</span>
            {item.detail && <span className="first-run-detail">{item.detail}</span>}
          </li>
        ))}
      </ul>
      <button type="button" className="first-run-cta" onClick={hasTarget ? onStartSession : onSetupTarget}>
        {hasTarget ? t("app.firstRun.startSession") : t("app.firstRun.addMachine")}
      </button>
      <p className="first-run-hint">
        {t("app.firstRun.hint")}
      </p>
      {onOpenConnectTools && (
        <button type="button" className="first-run-connect-link" onClick={onOpenConnectTools}>
          {t("app.firstRun.connectTools")}
        </button>
      )}
    </section>
  );
}
