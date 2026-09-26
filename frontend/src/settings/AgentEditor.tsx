import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import { Modal } from "../sessions/Modal";
import type { SettingsApi } from "./Settings";
import type { Target } from "../types";
export interface AgentSpec {
  name: string;
  command: string;
  builtin?: boolean;
  args?: string[];
  model_flag?: string;
  prompt_arg?: boolean;
  resume_args?: string[];
  resume_id_args?: string[];
  fork_args?: string[];
  yolo_args?: string[];
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
  description: string;
  install_hint: string;
  source: string;
  unverified?: string[];
  verified_at: string;
  installed: boolean;
  added: boolean;
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
    throw Error(`${label} must be a JSON array of strings.`);
  return x as string[];
}
function envText(a?: AgentSpec) {
  return Object.entries(a?.env || {})
    .map(([k, v]) => `${k}=${typeof v === "object" ? "••••" : String(v)}`)
    .join("\n");
}
function env(v: string, prior: Record<string, JsonValue> = {}) {
  const out = { ...prior };
  for (const raw of v.split(/\r?\n/)) {
    if (!raw.trim()) continue;
    const i = raw.indexOf("=");
    if (i < 1) throw Error("Environment lines must use KEY=value.");
    const k = raw.slice(0, i).trim(),
      val = raw.slice(i + 1);
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(k))
      throw Error(`Invalid environment name ${k}.`);
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
  const [name, setName] = useState(source?.name || ""),
    [command, setCommand] = useState(source?.command || ""),
    [fixed, setFixed] = useState(JSON.stringify(source?.args || [], null, 2)),
    [modelFlag, setModelFlag] = useState(source?.model_flag || ""),
    [providerURL, setProviderURL] = useState(typeof source?.env?.OPENAI_BASE_URL === "string" ? source.env.OPENAI_BASE_URL : ""),
    [promptArg, setPromptArg] = useState(Boolean(source?.prompt_arg)),
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
    [environment, setEnvironment] = useState(envText(source)),
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
      throw Error("Name and command are required.");
    let permissionArgs: unknown;
    try {
      permissionArgs = JSON.parse(permissions || "{}");
    } catch {
      throw Error("Permission arguments must be JSON.");
    }
    if (
      !permissionArgs ||
      Array.isArray(permissionArgs) ||
      typeof permissionArgs !== "object"
    )
      throw Error("Permission arguments must be a JSON object.");
    const spec: AgentSpec = {
      ...source,
      name: name.trim(),
      command: command.trim(),
      args: args(fixed, "Fixed arguments"),
      model_flag: modelFlag.trim(),
      prompt_arg: promptArg,
      resume_args: args(resume, "Resume arguments"),
      resume_id_args: args(resumeID, "Resume-by-ID arguments"),
      fork_args: args(forkArgs, "Fork arguments"),
      yolo_args: args(yolo, "Yolo arguments"),
      models_command: modelsCommand.trim(),
      trust_command: trustCommand.trim(),
      env: env(environment, { ...source?.env, ...(providerURL.trim() ? { OPENAI_BASE_URL: providerURL.trim() } : {}) }),
    };
    delete spec.builtin;
    if (acpOn) {
      if (!acpCommand.trim()) throw Error("ACP command is required.");
      spec.acp = {
        command: acpCommand.trim(),
        args: args(acpArgs, "ACP arguments"),
        env: env(acpEnvironment, source?.acp?.env),
      };
      delete spec.task;
    } else {
      delete spec.acp;
      if (taskOn)
        spec.task = {
          command: taskCommand.trim() || spec.command,
          args: args(taskArgs, "Task arguments"),
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
    setStatus("Saving runner…");
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
      aria-label={source ? `Edit agent ${source.name}` : "Add agent"}
      onCancel={(e) => {
        if (busy) e.preventDefault();
        else onClose();
      }}
    >
      <h2>{source ? "Edit agent" : "Add agent"}</h2>
      <button disabled={busy} onClick={onClose}>
        Close
      </button>
      {!source && (
        <label>
          Starter template (catalog — one click, then edit anything below)
          <select
            value={presetChoice}
            onChange={(e) => {
              setPresetChoice(e.target.value);
              preset(e.target.value);
            }}
          >
            <option value="custom">Custom runner</option>
            {catalog
              .filter((p) => !p.added)
              .map((p) => {
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
                // Only the two Zed npx adapters are ever actually disabled:
                // their launch definitively fails without npx on whatever
                // target runs them. A plain preset's own `installed` is
                // host-local (there is no per-target remote check), so it is
                // shown as a hint, never used to block a starter template —
                // the CLI may well be installed on a different target.
                const disabled = needsBinary ? targetKnown === false : false;
                const reason = needsBinary
                  ? targetKnown === false
                    ? ` — ${needsBinary} not detected on any target`
                    : ""
                  : !p.installed
                    ? " — not installed on this host"
                    : "";
                return (
                  <option key={p.name} value={p.name} disabled={disabled}>
                    {p.display_name}
                    {reason}
                  </option>
                );
              })}
          </select>
        </label>
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
                  <b>Unverified fields (best guess, check before relying on
                  them):</b> {p.unverified.join(", ")}.
                </>
              )}
              {" "}Source: {p.source} (checked {p.verified_at}).
              {!p.installed && <> Install: <code>{p.install_hint}</code></>}
            </p>
          );
        })()}
      <label>
        Name
        <input value={name} onChange={(e) => setName(e.target.value)} />
      </label>
      <label>
        Command
        <input value={command} onChange={(e) => setCommand(e.target.value)} />
      </label>
      <label>
        Fixed arguments
        <textarea value={fixed} onChange={(e) => setFixed(e.target.value)} />
      </label>
      <label>
        Model flag
        <input
          value={modelFlag}
          onChange={(e) => setModelFlag(e.target.value)}
        />
      </label>
      <label>
        Provider endpoint URL (optional)
        <input value={providerURL} onChange={(e) => setProviderURL(e.target.value)} />
      </label>
      <label>
        <input
          type="checkbox"
          checked={promptArg}
          onChange={(e) => setPromptArg(e.target.checked)}
        />{" "}
        Opening prompt is a positional argument
      </label>
      <label>
        Resume arguments (resume the CLI's own last conversation)
        <textarea value={resume} onChange={(e) => setResume(e.target.value)} />
      </label>
      <label>
        Resume-by-ID arguments (JSON array; {"{id}"}/{"{dir}"} substituted — blank if unsupported)
        <textarea value={resumeID} onChange={(e) => setResumeID(e.target.value)} />
      </label>
      <label>
        Fork arguments (JSON array; {"{id}"}/{"{dir}"} substituted — blank if unsupported)
        <textarea value={forkArgs} onChange={(e) => setForkArgs(e.target.value)} />
      </label>
      <label>
        Model catalog command (optional; {"{bin}"} is the resolved binary)
        <input value={modelsCommand} onChange={(e) => setModelsCommand(e.target.value)} />
      </label>
      <label>
        Trust command (optional; {"{dir}"} is the working directory)
        <input value={trustCommand} onChange={(e) => setTrustCommand(e.target.value)} />
      </label>
      <label>
        Yolo arguments
        <textarea value={yolo} onChange={(e) => setYolo(e.target.value)} />
      </label>
      <label>
        Environment (KEY=value lines; existing values are masked and retained)
        <textarea
          aria-label="Environment (KEY=value lines; existing values are masked and retained)"
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
        Enable background tasks for this agent
      </label>
      {acpOn && (
        <p className="sub">
          Disabled while ACP is enabled below — a background task is either a
          plain command or an ACP agent, never both.
        </p>
      )}
      {taskOn && !acpOn && (
        <>
          <label>
            Task command
            <input
              value={taskCommand}
              onChange={(e) => setTaskCommand(e.target.value)}
            />
          </label>
          <label>
            Task arguments (one per line; JSON array accepted)
            <textarea
              aria-label="Task arguments (one per line; JSON array accepted)"
              value={taskArgs}
              onChange={(e) => setTaskArgs(e.target.value)}
            />
          </label>
          <label>
            Prompt template
            <input
              aria-label="Prompt template"
              value={taskPrompt}
              onChange={(e) => setTaskPrompt(e.target.value)}
            />
          </label>
          <label>
            Task output
            <select
              value={taskOutput}
              onChange={(e) => setTaskOutput(e.target.value)}
            >
              <option value="plain">Plain text</option>
              <option value="jsonl">JSONL events</option>
            </select>
          </label>
          <label>
            Permission arguments
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
        Use the Agent Client Protocol (ACP) instead of a task command
      </label>
      {taskOn && !acpOn && (
        <p className="sub">
          Disabled while background tasks are enabled above.
        </p>
      )}
      {acpOn && (
        <>
          <p className="sub">
            Every permission mode is supported automatically — ACP carries
            its own gated approvals. See{" "}
            <a href="https://agentclientprotocol.com" target="_blank" rel="noreferrer">
              agentclientprotocol.com
            </a>
            .
          </p>
          <label>
            ACP command
            <input
              aria-label="ACP command"
              value={acpCommand}
              onChange={(e) => setAcpCommand(e.target.value)}
            />
          </label>
          <label>
            ACP arguments (one per line; JSON array accepted)
            <textarea
              aria-label="ACP arguments (one per line; JSON array accepted)"
              value={acpArgs}
              onChange={(e) => setAcpArgs(e.target.value)}
            />
          </label>
          <label>
            ACP environment (KEY=value lines; existing values are masked and retained)
            <textarea
              aria-label="ACP environment (KEY=value lines; existing values are masked and retained)"
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
        Save runner
      </button>
      <p className="agent-dialog-status" role="status">{status}</p>
      <button disabled={busy} onClick={onClose}>
        Cancel
      </button>
    </Modal>
  );
}
