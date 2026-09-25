import { useEffect, useState } from "react";

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
}

type Item = { label: string; ok: boolean; detail?: string };

// FirstRun is the checklist a brand-new install shows instead of an empty
// kanban board full of column headers and nothing else. It appears only
// while there is genuinely nothing to look at (no project, no session) —
// the moment either exists, the ordinary board/sessions views take over and
// this never shows again. Every row that can be wrong carries its own fix;
// the one thing it asks you to do is the same button, always available.
export function FirstRun({ request, hasProject, hasSession, hasTarget, onSetupTarget, onStartSession }: FirstRunProps) {
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
    { label: "A machine", ok: hasTarget, detail: hasTarget ? undefined : "add this machine or an SSH target in Settings" },
    {
      label: "Agent CLI on this server",
      ok: agentsOK,
      detail: agentsOK
        ? agentsFound.map((a) => a.name).join(", ")
        : "install Claude Code, Codex, or Gemini so Lectern has something to launch",
    },
    {
      label: "tmux ready",
      ok: !!status?.tmux.ok,
      detail: status?.tmux.ok ? status.tmux.detail : status?.tmux.fix,
    },
    {
      label: "git ready",
      ok: !!status?.git.ok,
      detail: status?.git.ok ? status.git.detail : status?.git.fix,
    },
    { label: "Python 3 ready", ok: !!status?.python?.ok, detail: status?.python?.ok ? status.python.detail : status?.python?.fix },
    { label: "A project", ok: hasProject, detail: hasProject ? undefined : "optional — a blank room works with no project" },
    { label: "First session", ok: hasSession },
  ];
  const loading = !status && !error;

  return (
    <section className="first-run" aria-labelledby="first-run-heading">
      <h2 id="first-run-heading">Get started</h2>
      <p className="first-run-sub">
        Lectern dispatches AI coding agents onto machines you own. Here's what's ready so far.
      </p>
      {error && (
        <p className="first-run-error">
          Couldn't read setup status ({error}) — you can still start a session.
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
        {hasTarget ? "Start your first session" : "Add your first machine"}
      </button>
      <p className="first-run-hint">
        For SSH machines, install and sign in to your agent CLI on that machine. Docker does
        not inherit host tools or logins. You can also open a blank shell without an agent.
      </p>
    </section>
  );
}
