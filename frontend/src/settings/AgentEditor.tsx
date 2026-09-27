import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import { Modal } from "../sessions/Modal";
import type { SettingsApi } from "./Settings";
import type { Target } from "../types";
import { capabilityChips, groupCatalog, type CatalogCapability } from "./agentCatalog";
import { t, useLocale } from "../i18n";
export interface AgentSpec {
  name: string;
  command: string;
  builtin?: boolean;
  args?: string[];
  model_flag?: string;
  prompt_arg?: boolean;
  prompt_args?: string[];
  resume_args?: string[];
  resume_id_args?: string[];
  fork_args?: string[];
  yolo_args?: string[];
  yolo_env?: Record<string, JsonValue>;
  models_command?: string;
  trust_command?: string;
  env?: Record<string, JsonValue>;
  task?: {
    command?: string;
    args?: string[];
    prompt_template?: string;
    output_mode?: string;
    permission_args?: Record<string, JsonValue>;
  };
  // acp makes a background task on this agent run through the Agent Client
  // Protocol (agentclientprotocol.com) instead of `task` above — mutually
  // exclusive with it. See docs/acp.md.
  acp?: {
    command?: string;
    args?: string[];
    env?: Record<string, JsonValue>;
  };
}
// CatalogPreset mirrors internal/sessions.CatalogPreset's JSON shape — a
// popular third-party CLI's starter Spec plus how much of it was actually
// verified against the vendor's own docs (see that Go type's doc comment for
// what Unverified means). Settings → Agents fetches these from
// GET /api/agents/catalog for the "Add from catalog" starter list, replacing
// what used to be a handful of presets hardcoded in this file.
export interface CatalogPreset extends AgentSpec {
  display_name: string;
  vendor?: string;
  group?: string;
  icon?: { glyph: string; color: string };
  homepage?: string;
  description: string;
  install_hint: string;
  sessions_hint?: string;
  source: string;
  verified_by?: string;
  unverified?: string[];
  verified_at: string;
  installed: boolean;
  added: boolean;
  capabilities?: Record<string, CatalogCapability>;
}
// targetHasBinary reports whether any target's last probe found `key` — used
// to grey out an ACP preset whose binary nothing has confirmed yet, rather
// than letting the operator save a runner that can only fail at dispatch.
// Unprobed (empty info_json) is treated as "unknown", not "missing" — a
// fresh target with no check run yet must not permanently greyed-out every
// preset.
export function targetHasBinary(targets: Target[], key: string): boolean | undefined {
  if (targets.length === 0) return undefined;
  let probed = false;
  for (const t of targets) {
    if (!t.info_json) continue;
    let info: Record<string, JsonValue>;
    try {
      info = JSON.parse(t.info_json);
    } catch {
      continue;
    }
    if (!(key in info)) continue;
    probed = true;
    if (info[key]) return true;
  }
  return probed ? false : undefined;
}
function args(v: string, label: string) {
  if (!v.trim()) return [];
  let x: unknown;
  try {
    x = JSON.parse(v);
  } catch {
    x = v
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean);
  }
  if (!Array.isArray(x) || x.some((y) => typeof y !== "string"))
    throw Error(t("agentSettings.editor.argsNotArray", { label }));
  return x as string[];
}
function envText(a?: AgentSpec) {
  return mapText(a?.env);
}
function mapText(m?: Record<string, JsonValue>) {
  return Object.entries(m || {})
    .map(([k, v]) => `${k}=${typeof v === "object" ? "••••" : String(v)}`)
    .join("\n");
}
function env(v: string, prior: Record<string, JsonValue> = {}) {
  const out = { ...prior };
  for (const raw of v.split(/\r?\n/)) {
    if (!raw.trim()) continue;
    const i = raw.indexOf("=");
    if (i < 1) throw Error(t("agentSettings.editor.envLineFormat"));
    const k = raw.slice(0, i).trim(),
      val = raw.slice(i + 1);
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k))
      throw Error(t("agentSettings.editor.envInvalidName", { name: k }));
    if (val === "••••" || val === "***") continue;
    if (val === "") delete out[k];
    else out[k] = val;
  }
  return out;
}
export function AgentEditor({
  api,
  source,
  all,
  targets,
  onClose,
  onSaved,
  onNotice,
}: {
  api: SettingsApi;
  source?: AgentSpec;
  all: AgentSpec[];
  targets?: Target[];
  onClose(): void;
  onSaved(): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [name, setName] = useState(source?.name || ""),
    [command, setCommand] = useState(source?.command || ""),
    [fixed, setFixed] = useState(JSON.stringify(source?.args || [], null, 2)),
    [modelFlag, setModelFlag] = useState(source?.model_flag || ""),
    [providerURL, setProviderURL] = useState(typeof source?.env?.OPENAI_BASE_URL === "string" ? source.env.OPENAI_BASE_URL : ""),
    [promptArg, setPromptArg] = useState(Boolean(source?.prompt_arg)),
    [promptArgs, setPromptArgs] = useState(
      JSON.stringify(source?.prompt_args || [], null, 2),
    ),
    [resume, setResume] = useState(
      JSON.stringify(source?.resume_args || [], null, 2),
    ),
    [resumeID, setResumeID] = useState(
      JSON.stringify(source?.resume_id_args || [], null, 2),
    ),
    [forkArgs, setForkArgs] = useState(
      JSON.stringify(source?.fork_args || [], null, 2),
    ),
    [modelsCommand, setModelsCommand] = useState(source?.models_command || ""),
    [trustCommand, setTrustCommand] = useState(source?.trust_command || ""),
    [yolo, setYolo] = useState(
      JSON.stringify(source?.yolo_args || [], null, 2),
    ),
    [yoloEnv, setYoloEnv] = useState(mapText(source?.yolo_env)),
    [environment, setEnvironment] = useState(envText(source)),
    [query, setQuery] = useState(""),
    [catalog, setCatalog] = useState<CatalogPreset[]>([]),
    [taskOn, setTaskOn] = useState(Boolean(source?.task)),
    [taskCommand, setTaskCommand] = useState(source?.task?.command || ""),
    [taskArgs, setTaskArgs] = useState(
      JSON.stringify(source?.task?.args || [], null, 2),
    ),
    [taskPrompt, setTaskPrompt] = useState(source?.task?.prompt_template || ""),
    [taskOutput, setTaskOutput] = useState(
      source?.task?.output_mode || "plain",
    ),
    [permissions, setPermissions] = useState(
      JSON.stringify(source?.task?.permission_args || {}, null, 2),
    ),
    // acp and task are mutually exclusive non-interactive invocations (see
    // docs/acp.md); acpOn defaults from whichever the loaded spec actually
    // has, so an existing task-backed agent is never silently switched.
    [acpOn, setAcpOn] = useState(Boolean(source?.acp)),
    [acpCommand, setAcpCommand] = useState(source?.acp?.command || ""),
    [acpArgs, setAcpArgs] = useState(
      JSON.stringify(source?.acp?.args || [], null, 2),
    ),
    [acpEnvironment, setAcpEnvironment] = useState(
      Object.entries(source?.acp?.env || {})
        .map(([k, v]) => `${k}=${typeof v === "object" ? "••••" : String(v)}`)
        .join("\n"),
    ),
    [busy, setBusy] = useState(false),
    [status, setStatus] = useState(""),
    [presetChoice, setPresetChoice] = useState("custom");
  useEffect(() => {
    if (source) return; // catalog only matters for "Add agent"
    api
      .request<CatalogPreset[]>("/agents/catalog")
      .then(setCatalog)
      .catch(() => setCatalog([]));
  }, []);
  // preset fills every field this form has from one catalog entry — the
  // whole point of GET /api/agents/catalog is that Settings never hardcodes
  // a CLI's flags itself, so a researched preset (and any future one added
  // server-side) shows up here with zero frontend changes.
  function preset(v: string) {
    if (v === "custom") return;
    const p = catalog.find((c) => c.name === v);
    if (!p) return;
    setName(p.name);
    setCommand(p.command);
    setFixed(JSON.stringify(p.args || [], null, 2));
    setModelFlag(p.model_flag || "");
    setPromptArg(Boolean(p.prompt_arg));
    setPromptArgs(JSON.stringify(p.prompt_args || [], null, 2));
    setYoloEnv(mapText(p.yolo_env));
    setResume(JSON.stringify(p.resume_args || [], null, 2));
    setResumeID(JSON.stringify(p.resume_id_args || [], null, 2));
    setForkArgs(JSON.stringify(p.fork_args || [], null, 2));
    setYolo(JSON.stringify(p.yolo_args || [], null, 2));
    setModelsCommand(p.models_command || "");
    setTrustCommand(p.trust_command || "");
    setEnvironment(
      Object.entries(p.env || {})
        .map(([k, v2]) => `${k}=${String(v2)}`)
        .join("\n"),
    );
    if (p.acp) {
      setAcpOn(true);
      setTaskOn(false);
      setAcpCommand(p.acp.command || "");
      setAcpArgs(JSON.stringify(p.acp.args || [], null, 2));
      setAcpEnvironment(
        Object.entries(p.acp.env || {})
          .map(([k, v2]) => `${k}=${String(v2)}`)
          .join("\n"),
      );
    } else {
      setAcpOn(false);
      if (p.task) {
        setTaskOn(true);
        setTaskCommand(p.task.command || "");
        setTaskArgs(JSON.stringify(p.task.args || [], null, 2));
        setTaskPrompt(p.task.prompt_template || "");
        setTaskOutput(p.task.output_mode || "plain");
        setPermissions(JSON.stringify(p.task.permission_args || {}, null, 2));
      } else {
        setTaskOn(false);
      }
    }
  }
  // A preset needing npx (both Zed adapters) is disabled until a target has
  // actually confirmed it; the gemini-acp preset needs `gemini` itself,
  // since there is no npm-packaged adapter for it. `undefined` (never
  // probed) is left enabled rather than blocked — the operator may be
  // adding the very first target.
  const npxAvailable = targetHasBinary(targets || [], "npx"),
    geminiAvailable = targetHasBinary(targets || [], "gemini");
  async function save() {
    if (!name.trim() || !command.trim())
      throw Error(t("agentSettings.editor.nameCommandRequired"));
    let permissionArgs: unknown;
    try {
      permissionArgs = JSON.parse(permissions || "{}");
    } catch {
      throw Error(t("agentSettings.editor.permissionsNotJson"));
    }
    if (
      !permissionArgs ||
      Array.isArray(permissionArgs) ||
      typeof permissionArgs !== "object"
    )
      throw Error(t("agentSettings.editor.permissionsNotObject"));
    const spec: AgentSpec = {
      ...source,
      name: name.trim(),
      command: command.trim(),
      args: args(fixed, t("agentSettings.editor.fixedArgs")),
      model_flag: modelFlag.trim(),
      prompt_arg: promptArg,
      prompt_args: args(promptArgs, t("agentSettings.editor.promptArgsName")),
      resume_args: args(resume, t("agentSettings.editor.resumeArgsName")),
      resume_id_args: args(resumeID, t("agentSettings.editor.resumeIdArgsName")),
      fork_args: args(forkArgs, t("agentSettings.editor.forkArgsName")),
      yolo_args: args(yolo, t("agentSettings.editor.yoloArgs")),
      yolo_env: env(yoloEnv, source?.yolo_env),
      models_command: modelsCommand.trim(),
      trust_command: trustCommand.trim(),
      env: env(environment, { ...source?.env, ...(providerURL.trim() ? { OPENAI_BASE_URL: providerURL.trim() } : {}) }),
    };
    delete spec.builtin;
    if (acpOn) {
      if (!acpCommand.trim()) throw Error(t("agentSettings.editor.acpCommandRequired"));
      spec.acp = {
        command: acpCommand.trim(),
        args: args(acpArgs, t("agentSettings.editor.acpArgsName")),
        env: env(acpEnvironment, source?.acp?.env),
      };
      delete spec.task;
    } else {
      delete spec.acp;
      if (taskOn)
        spec.task = {
          command: taskCommand.trim() || spec.command,
          args: args(taskArgs, t("agentSettings.editor.taskArgsName")),
          prompt_template: taskPrompt.trim(),
          output_mode: taskOutput,
          permission_args: permissionArgs as Record<string, JsonValue>,
        };
      else delete spec.task;
    }
    const custom = all
      .filter((x) => !x.builtin && x.name !== source?.name)
      .map((x) => {
        const y = { ...x };
        delete y.builtin;
        return y;
      });
    setBusy(true);
    setStatus(t("agentSettings.editor.saving"));
    try {
      await api.request("/agents", {
        method: "PUT",
        body: [...custom, spec] as unknown as JsonValue,
      });
      await onSaved();
      onClose();
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <Modal
      className="agent-settings-dialog"
      aria-label={source ? t("agentSettings.editor.editAgentNamed", { name: source.name }) : t("agentSettings.editor.addAgent")}
      onCancel={(e) => {
        if (busy) e.preventDefault();
        else onClose();
      }}
    >
      <h2>{source ? t("agentSettings.editor.editAgent") : t("agentSettings.editor.addAgent")}</h2>
      <button disabled={busy} onClick={onClose}>
        {t("agentSettings.editor.close")}
      </button>
      {!source && (
        <section className="agent-catalog" aria-label={t("agentSettings.editor.catalog")}>
          <label>
            {t("agentSettings.editor.starterTemplate")}
            <input
              type="search"
              aria-label={t("agentSettings.editor.searchCatalog")}
              placeholder={t("agentSettings.editor.searchPlaceholder", { n: catalog.length || "" })}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </label>
          <div className="agent-catalog-list">
            <button
              type="button"
              className="agent-catalog-item agent-catalog-custom"
              aria-pressed={presetChoice === "custom"}
              onClick={() => setPresetChoice("custom")}
            >
              <span className="agent-catalog-icon" aria-hidden="true">+</span>
              <span className="agent-catalog-text">
                <b>{t("agentSettings.editor.customRunner")}</b>
                <span className="agent-catalog-meta">{t("agentSettings.editor.customRunnerHint")}</span>
              </span>
            </button>
            {groupCatalog(catalog, query).map((section) => (
              <div key={section.group} className="agent-catalog-group" role="group" aria-label={section.group === "Other" ? t("agentSettings.editor.groupOther") : section.group}>
                <h3>{section.group === "Other" ? t("agentSettings.editor.groupOther") : section.group}</h3>
                {section.items.map((p) => {
                  // The two Zed adapter presets need npx, not their own
                  // placeholder Command; gemini-acp needs `gemini` itself.
                  // Everything else's Installed() already checked the CLI
                  // this preset actually launches.
                  const needsBinary =
                    p.name === "claude-code-acp" || p.name === "codex-acp"
                      ? "npx"
                      : p.name === "gemini-acp"
                        ? "gemini"
                        : "";
                  const targetKnown = needsBinary
                    ? targetHasBinary(targets || [], needsBinary)
                    : undefined;
                  // Only the two Zed npx adapters are ever disabled for a
                  // binary: their launch definitively fails without npx on
                  // whatever target runs them. A plain preset's own
                  // `installed` is host-local (there is no per-target remote
                  // check), so it is a hint, never a block — the CLI may well
                  // be installed on a different target.
                  const disabled = p.added || (needsBinary ? targetKnown === false : false);
                  const note = p.added
                    ? t("agentSettings.editor.alreadyAdded")
                    : needsBinary && targetKnown === false
                      ? t("agentSettings.editor.binaryNotDetected", { binary: needsBinary })
                      : p.installed
                        ? t("agentSettings.editor.installedHere")
                        : "";
                  return (
                    <button
                      type="button"
                      key={p.name}
                      data-preset={p.name}
                      className="agent-catalog-item"
                      aria-pressed={presetChoice === p.name}
                      disabled={disabled}
                      onClick={() => {
                        setPresetChoice(p.name);
                        preset(p.name);
                      }}
                    >
                      <span
                        className="agent-catalog-icon"
                        aria-hidden="true"
                        style={p.icon ? { background: p.icon.color } : undefined}
                      >
                        {p.icon?.glyph || p.display_name.slice(0, 1)}
                      </span>
                      <span className="agent-catalog-text">
                        <b>{p.display_name}</b>
                        <span className="agent-catalog-meta">
                          {p.vendor ? `${p.vendor} · ` : ""}
                          <code>{p.command}</code>
                          {note && ` · ${note}`}
                        </span>
                        <span className="agent-catalog-chips">
                          {capabilityChips(p).map((c) => (
                            <span
                              key={c.key}
                              className={c.available ? "agent-cap on" : "agent-cap off"}
                              title={c.title}
                            >
                              {c.available ? c.label : t("agentSettings.editor.capabilityMissing", { label: c.label })}
                            </span>
                          ))}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
            ))}
            {catalog.length > 0 && groupCatalog(catalog, query).length === 0 && (
              <p className="sub">
                {t("agentSettings.editor.noMatch", { query })}
              </p>
            )}
          </div>
        </section>
      )}
      {!source &&
        presetChoice !== "custom" &&
        (() => {
          const p = catalog.find((c) => c.name === presetChoice);
          if (!p) return null;
          return (
            <p className="sub agent-preset-hint">
              {p.description}
              {p.unverified && p.unverified.length > 0 && (
                <>
                  {" "}
                  <b>{t("agentSettings.editor.unverifiedFields")}</b> {p.unverified.join(", ")}.
                </>
              )}
              {p.sessions_hint && <>{" "}{t("agentSettings.editor.sessionIds")}{" "}<code>{p.sessions_hint}</code>.</>}
              {" "}{t("agentSettings.editor.checkedAgainst", { date: p.verified_at, by: p.verified_by === "docs" ? t("agentSettings.editor.vendorDocs") : p.verified_by || t("agentSettings.editor.itsDocs"), source: p.source })}
              {!p.installed && <>{" "}{t("agentSettings.editor.install")}{" "}<code>{p.install_hint}</code></>}
            </p>
          );
        })()}
      <label>
        {t("agentSettings.editor.name")}
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.command")}
        <input value={command} onChange={(e) => setCommand(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.fixedArgs")}
        <textarea value={fixed} onChange={(e) => setFixed(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.modelFlag")}
        <input
          value={modelFlag}
          onChange={(e) => setModelFlag(e.target.value)}
        />
      </label>
      <label>
        {t("agentSettings.editor.providerUrl")}
        <input value={providerURL} onChange={(e) => setProviderURL(e.target.value)} />
      </label>
      <label>
        <input
          type="checkbox"
          checked={promptArg}
          onChange={(e) => setPromptArg(e.target.checked)}
        />{" "}
        {t("agentSettings.editor.promptPositional")}
      </label>
      <label>
        {t("agentSettings.editor.promptArgs")}
        <textarea value={promptArgs} onChange={(e) => setPromptArgs(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.resumeArgs")}
        <textarea value={resume} onChange={(e) => setResume(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.resumeIdArgs")}
        <textarea value={resumeID} onChange={(e) => setResumeID(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.forkArgs")}
        <textarea value={forkArgs} onChange={(e) => setForkArgs(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.modelsCommand")}
        <input value={modelsCommand} onChange={(e) => setModelsCommand(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.trustCommand")}
        <input value={trustCommand} onChange={(e) => setTrustCommand(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.yoloArgs")}
        <textarea value={yolo} onChange={(e) => setYolo(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.yoloEnv")}
        <textarea value={yoloEnv} onChange={(e) => setYoloEnv(e.target.value)} />
      </label>
      <label>
        {t("agentSettings.editor.environment")}
        <textarea
          aria-label={t("agentSettings.editor.environment")}
          value={environment}
          onChange={(e) => setEnvironment(e.target.value)}
        />
      </label>
      <label>
        <input
          type="checkbox"
          checked={taskOn}
          disabled={acpOn}
          onChange={(e) => setTaskOn(e.target.checked)}
        />{" "}
        {t("agentSettings.editor.enableTasks")}
      </label>
      {acpOn && (
        <p className="sub">
          {t("agentSettings.editor.tasksDisabledByAcp")}
        </p>
      )}
      {taskOn && !acpOn && (
        <>
          <label>
            {t("agentSettings.editor.taskCommand")}
            <input
              value={taskCommand}
              onChange={(e) => setTaskCommand(e.target.value)}
            />
          </label>
          <label>
            {t("agentSettings.editor.taskArgs")}
            <textarea
              aria-label={t("agentSettings.editor.taskArgs")}
              value={taskArgs}
              onChange={(e) => setTaskArgs(e.target.value)}
            />
          </label>
          <label>
            {t("agentSettings.editor.promptTemplate")}
            <input
              aria-label={t("agentSettings.editor.promptTemplate")}
              value={taskPrompt}
              onChange={(e) => setTaskPrompt(e.target.value)}
            />
          </label>
          <label>
            {t("agentSettings.editor.taskOutput")}
            <select
              value={taskOutput}
              onChange={(e) => setTaskOutput(e.target.value)}
            >
              <option value="plain">{t("agentSettings.editor.outputPlain")}</option>
              <option value="jsonl">{t("agentSettings.editor.outputJsonl")}</option>
            </select>
          </label>
          <label>
            {t("agentSettings.editor.permissionArgs")}
            <textarea
              value={permissions}
              onChange={(e) => setPermissions(e.target.value)}
            />
          </label>
        </>
      )}
      <label>
        <input
          type="checkbox"
          checked={acpOn}
          disabled={taskOn}
          onChange={(e) => setAcpOn(e.target.checked)}
        />{" "}
        {t("agentSettings.editor.useAcp")}
      </label>
      {taskOn && !acpOn && (
        <p className="sub">
          {t("agentSettings.editor.acpDisabledByTasks")}
        </p>
      )}
      {acpOn && (
        <>
          <p className="sub">
            {t("agentSettings.editor.acpPermissions")}{" "}
            <a href="https://agentclientprotocol.com" target="_blank" rel="noreferrer">
              agentclientprotocol.com
            </a>
            .
          </p>
          <label>
            {t("agentSettings.editor.acpCommand")}
            <input
              aria-label={t("agentSettings.editor.acpCommand")}
              value={acpCommand}
              onChange={(e) => setAcpCommand(e.target.value)}
            />
          </label>
          <label>
            {t("agentSettings.editor.acpArgs")}
            <textarea
              aria-label={t("agentSettings.editor.acpArgs")}
              value={acpArgs}
              onChange={(e) => setAcpArgs(e.target.value)}
            />
          </label>
          <label>
            {t("agentSettings.editor.acpEnvironment")}
            <textarea
              aria-label={t("agentSettings.editor.acpEnvironment")}
              value={acpEnvironment}
              onChange={(e) => setAcpEnvironment(e.target.value)}
            />
          </label>
        </>
      )}
      <button
        disabled={busy}
        onClick={() => void save().catch((e) => { const message=e instanceof Error?e.message:String(e);setStatus(message);onNotice(message, true); })}
      >
        {t("agentSettings.editor.saveRunner")}
      </button>
      <p className="agent-dialog-status" role="status">{status}</p>
      <button disabled={busy} onClick={onClose}>
        {t("agentSettings.editor.cancel")}
      </button>
    </Modal>
  );
}
