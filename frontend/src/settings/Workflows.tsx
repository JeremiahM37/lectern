import { useEffect, useRef, useState } from "react";
import type { SettingsApi } from "./Settings";
import { t, useLocale } from "../i18n";

export interface ProjectWorkflow {
  id: "spec-kit" | "maestro" | string;
  name: string;
  description: string;
  version: string;
  upstream_url: string;
  enabled: boolean;
  commands: string[];
}

interface WorkflowsResponse {
  workflows?: ProjectWorkflow[];
  reload_required?: boolean;
}

const providerLabel = (agent: string) => (agent === "codex" ? "Codex" : "Claude Code");
const displayCommand = (command: string, agent: string) => {
  if (command.startsWith("/") || command.startsWith("$")) return command;
  return `${agent === "codex" ? "$" : "/"}${command}`;
};

export function Workflows({
  api,
  projectId,
  defaultAgent,
  onNotice,
}: {
  api: SettingsApi;
  projectId: number;
  defaultAgent: string;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [agent, setAgent] = useState(defaultAgent === "codex" ? "codex" : "claude");
  const [workflows, setWorkflows] = useState<ProjectWorkflow[]>([]);
  const [busy, setBusy] = useState(false);
  const [hasError, setHasError] = useState(false);
  const [status, setStatus] = useState(() => t("agentSettings.workflows.loading"));
  const generation = useRef(0);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  async function load() {
    const mine = ++generation.current;
    setBusy(true);
    setHasError(false);
    setStatus(t("agentSettings.workflows.loading"));
    try {
      const result = await api.request<WorkflowsResponse>(
        `/projects/${projectId}/workflows?agent=${encodeURIComponent(agent)}`,
      );
      if (!mounted.current || mine !== generation.current) return;
      const rows = Array.isArray(result.workflows) ? result.workflows : [];
      setWorkflows(rows);
      setStatus(t("agentSettings.workflows.available", { count: rows.length, provider: providerLabel(agent) }));
    } catch (error) {
      if (!mounted.current || mine !== generation.current) return;
      const message = error instanceof Error ? error.message : String(error);
      setWorkflows([]);
      setHasError(true);
      setStatus(message);
      onNotice(message, true);
    } finally {
      if (mounted.current && mine === generation.current) setBusy(false);
    }
  }

  useEffect(() => {
    void load();
  }, [agent, projectId]);

  async function toggle(workflow: ProjectWorkflow) {
    const mine = generation.current;
    setBusy(true);
    try {
      const result = await api.request<{ enabled: boolean; reload_required?: boolean }>(
        `/projects/${projectId}/workflows/${encodeURIComponent(workflow.id)}`,
        { method: "PUT", body: { agent, enabled: !workflow.enabled } },
      );
      // A provider/project change while this request was in flight must win.
      if (mounted.current && mine === generation.current) {
        setWorkflows((rows) =>
          rows.map((row) =>
            row.id === workflow.id ? { ...row, enabled: Boolean(result.enabled) } : row,
          ),
        );
        setStatus(
          result.enabled
            ? t("agentSettings.workflows.enabledStatus", { name: workflow.name, provider: providerLabel(agent) })
            : t("agentSettings.workflows.disabledStatus", { name: workflow.name, provider: providerLabel(agent) }),
        );
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (mounted.current && mine === generation.current) {
        setStatus(message);
        onNotice(message, true);
      }
    } finally {
      if (mounted.current && mine === generation.current) setBusy(false);
    }
  }

  return (
    <section className="project-workflows" data-setting="projects.workflows">
      <h4>{t("agentSettings.workflows.title")}</h4>
      <p>
        {t("agentSettings.workflows.intro")}
      </p>
      <p className="workflows-status" role="status">{status}</p>
      <label>
        {t("agentSettings.skills.provider")}
        <select
          className="workflows-agent"
          aria-label={t("agentSettings.workflows.providerLabel")}
          value={agent}
          disabled={busy}
          onChange={(event) => setAgent(event.target.value)}
        >
          <option value="claude">Claude Code</option>
          <option value="codex">Codex</option>
        </select>
      </label>
      <button className="workflows-reload" disabled={busy} onClick={() => void load()}>
        {busy ? t("agentSettings.workflows.loadingShort") : hasError ? t("agentSettings.workflows.retry") : t("agentSettings.skills.reload")}
      </button>
      {!workflows.length && !busy && (
        <p>{t("agentSettings.workflows.none")}</p>
      )}
      {workflows.map((workflow) => (
        <article className="workflow-card" key={workflow.id}>
          <div className="workflow-card-copy">
            <h5>{workflow.name}</h5>
            <p>{workflow.description}</p>
            <p className="workflow-version">
              {t("agentSettings.workflows.pinnedVersion")}{" "}<code>{workflow.version}</code>{" · "}
              <a href={workflow.upstream_url} target="_blank" rel="noreferrer">
                {t("agentSettings.workflows.upstream")}
              </a>
            </p>
          </div>
          <button
            className="workflow-toggle"
            aria-label={workflow.enabled ? t("agentSettings.workflows.disableNamed", { name: workflow.name }) : t("agentSettings.workflows.enableNamed", { name: workflow.name })}
            disabled={busy}
            onClick={() => void toggle(workflow)}
          >
            {workflow.enabled ? t("agentSettings.workflows.enabled") : t("agentSettings.workflows.enable")}
          </button>
          {workflow.enabled && (
            <div className="workflow-commands">
              <b>{t("agentSettings.workflows.commands")}</b>
              <ul>
                {workflow.commands.map((command) => (
                  <li key={command}><code>{displayCommand(command, agent)}</code></li>
                ))}
              </ul>
              <p className="workflow-reload-note">
                {t("agentSettings.workflows.reloadNote", { provider: providerLabel(agent) })}
              </p>
            </div>
          )}
        </article>
      ))}
    </section>
  );
}
