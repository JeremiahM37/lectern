import { useEffect, useMemo, useState } from "react";
import type { Claim, Project, TaskView } from "../types";
import type { BoardApi } from "./Board";
import { Modal } from "../sessions/Modal";
import { claimScopeLabel } from "../claims/ClaimsPanel";
import { fetchAgentMenu, splitAgentMenu } from "../agents/menu";
import { AllAgentsPicker } from "../agents/AllAgentsPicker";
import { availableAgents, raceAgents, type RaceMode } from "../remote/race";
import "../claims/claims.css";
import { t, useLocale } from "../i18n";
import "./board.css";

type AgentSpec = {
  name: string;
  builtin?: boolean;
  task?: unknown;
  model_flag?: string;
};
type Orchestration = {
  orchestrate_ready?: boolean;
  worker_problem?: string;
  settings?: { enabled?: boolean; lead_agent?: string; worker_agent?: string; worker_model?: string };
};
type Template = {
  name: string;
  title?: string;
  prompt?: string;
  permission_mode?: string;
  model?: string;
};
type LaunchProfile = { id: number; name: string; agent: string; model?: string };
// ExtraVariant is one Best-of-N attempt beyond the "main" one described by the
// form's own agent/model/permission fields — together they become the
// `variants` array a dispatch sends, capped at maxVariants total.
type ExtraVariant = {
  key: number;
  agent: string;
  model: string;
  permissionMode: string;
  launchProfile: string;
};
const maxVariants = 8;
let variantKeySeq = 0;
export function CreateTask({
  api,
  projects,
  onClose,
  onCreated,
  onChat,
  onNotice,
  onCompare,
  initialRace = 0,
}: {
  api: BoardApi;
  projects: Project[];
  onClose(): void;
  onCreated(): void;
  onChat(t: TaskView): void;
  onNotice(t: string, e?: boolean): void;
  // Race N agents lands in the task's Compare view.
  onCompare?(t: TaskView): void;
  initialRace?: number;
}) {
  useLocale();
  const [projectId, setProjectId] = useState(projects[0]?.id ?? 0),
    [title, setTitle] = useState(""),
    [prompt, setPrompt] = useState(""),
    [permission, setPermission] = useState("acceptEdits"),
    [agent, setAgent] = useState("claude"),
    [model, setModel] = useState(""),
    [variants, setVariants] = useState<ExtraVariant[]>([]),
    [priority, setPriority] = useState(2),
    [agents, setAgents] = useState<AgentSpec[]>([]),
    [templates, setTemplates] = useState<Template[]>([]),
    [profiles, setProfiles] = useState<LaunchProfile[]>([]),
    // budget (docs/budgets.md): an optional per-task spend cap, sent at
    // create. Empty string means "no cap" — kept as a string so the field
    // can be genuinely blank rather than defaulting to 0.
    [budget, setBudget] = useState(""),
    [budgetStatus, setBudgetStatus] = useState<{ any_blocked: boolean }>(),
    // The capability line: "checking" while it loads, the server's answer, or
    // nothing. Kept as data so the words follow the chosen language.
    [capInfo, setCapInfo] = useState<"checking" | { profile: string; mcp_servers: string[]; memory_dir: string } | null>(null),
    // Orchestrate: the same switch the quick bar has, with the rest of the
    // form choosing the lead instead of the worker.
    [orchestrate, setOrchestrate] = useState(false),
    [orchestration, setOrchestration] = useState<Orchestration>(),
    // Claim board (docs/claims.md point 4c): warn before launch if the
    // prompt looks like an active topic claim someone else already has —
    // advisory only, never blocks the dispatch.
    [overlaps, setOverlaps] = useState<Claim[]>([]),
    [agentMenu, setAgentMenu] = useState<string[]>([]),
    [showAllAgents, setShowAllAgents] = useState(false),
    [showAllVariantAgents, setShowAllVariantAgents] = useState<number | null>(null),
    [race, setRace] = useState(0),
    [raceMode, setRaceMode] = useState<RaceMode>("mixed"),
    [installed, setInstalled] = useState<Record<number, string[]>>({});
  const project = projects.find((p) => p.id === projectId);
  const eligible = useMemo(
    () => agents.filter((a) => a.builtin || a.task),
    [agents],
  );
  const { shown: shownAgents, more: moreAgents } = useMemo(() => {
    const pool = eligible.length
      ? eligible
      : [{ name: "claude" }, { name: "codex" }, { name: "gemini" }];
    return splitAgentMenu(pool, agentMenu);
  }, [eligible, agentMenu]);
  useEffect(() => {
    void Promise.all([
      api.request<AgentSpec[]>("/agents"),
      api.request<Template[]>("/templates"),
      api.request<LaunchProfile[]>("/launch-profiles"),
    ])
      .then(([a, list, p]) => {
        setAgents(a);
        setTemplates(list);
        setProfiles(p);
      })
      .catch(() => {});
    void fetchAgentMenu(api).then(setAgentMenu);
    void api
      .request<{ id: number; info_json: string }[]>("/targets")
      .then((ts) => setInstalled(Object.fromEntries(ts.map((t) => [t.id, availableAgents(t.info_json)]))))
      .catch(() => {});
    void api
      .request<Orchestration>("/delegation")
      .then((v) => setOrchestration(v && typeof v.orchestrate_ready === "boolean" ? v : { orchestrate_ready: false }))
      .catch(() => setOrchestration({ orchestrate_ready: false }));
    // Budgets (docs/budgets.md): a proactive note when a stop-mode limit is
    // already exhausted, so the refusal a blocked dispatch gets isn't a
    // surprise. Best-effort — an unreachable /api/budgets must not block
    // opening the form.
    void api
      .request<{ any_blocked: boolean }>("/budgets")
      .then(setBudgetStatus)
      .catch(() => {});
  }, []);
  useEffect(() => {
    if (!project) return;
    setAgent(project.default_agent || "claude");
    setPermission(project.default_permission_mode || "acceptEdits");
    setCapInfo("checking");
    void api
      .request<{ profile: string; mcp_servers: string[]; memory_dir: string }>(
        `/projects/${project.id}/capability`,
      )
      .then((c) => setCapInfo(c))
      .catch(() => setCapInfo(null));
  }, [projectId]);
  useEffect(() => {
    const text = `${title} ${prompt}`.trim();
    if (!projectId || text.length < 8) {
      setOverlaps([]);
      return;
    }
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      void api
        .request<Claim[]>(
          `/claims/topic-overlap?project_id=${projectId}&text=${encodeURIComponent(text)}`,
          { signal: controller.signal },
        )
        .then(setOverlaps)
        .catch(() => {});
    }, 500);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [projectId, title, prompt]);
  function applyRace(n: number, mode: RaceMode = raceMode) {
    setRace(n);
    setRaceMode(mode);
    const agents = raceAgents(n, agent, (project && installed[project.target_id]) || [], mode);
    setVariants(
      agents.slice(1).map((a) => ({
        key: ++variantKeySeq,
        agent: a === agent ? "" : a,
        model: "",
        permissionMode: "",
        launchProfile: "",
      })),
    );
  }
  useEffect(() => {
    if (initialRace && project) applyRace(initialRace);
  }, [initialRace, project?.id, installed]);
  async function create(dispatch: boolean, chat = false, compare = false) {
    const effectiveTitle = title.trim() || (compare ? prompt.trim().split("\n")[0]!.slice(0, 70) : "");
    if (!effectiveTitle) return onNotice(compare ? t("remote.race.describeFirst") : t("board.create.titleRequired"), true);
    const usesFable =
      model === "fable" || variants.some((v) => v.model === "fable");
    if (
      dispatch &&
      usesFable &&
      !confirm(
        t("board.create.fableConfirm"),
      )
    )
      return;
    const budgetUSD = budget.trim() === "" ? undefined : Number(budget);
    if (budgetUSD !== undefined && (!Number.isFinite(budgetUSD) || budgetUSD < 0))
      return onNotice(t("board.create.budgetInvalid"), true);
    try {
      const task = await api.createTask({
        project_id: projectId,
        title: effectiveTitle,
        prompt: prompt.trim(),
        priority,
        agent,
        model,
        permission_mode: permission,
        ...(orchestrate ? { orchestrate: true } : {}),
        ...(budgetUSD !== undefined ? { budget_usd: budgetUSD } : {}),
      });
      if (dispatch)
        await api.request(`/tasks/${task.id}/dispatch`, {
          method: "POST",
          body: {
            ...(variants.length > 0
              ? {
                  variants: [
                    { agent, model, permission_mode: permission },
                    ...variants.map((v) => ({
                      agent: v.agent,
                      model: v.model,
                      permission_mode: v.permissionMode,
                      launch_profile: v.launchProfile,
                    })),
                  ],
                }
              : {}),
            ...(budgetUSD !== undefined ? { budget_usd: budgetUSD } : {}),
          },
        });
      onCreated();
      onClose();
      onNotice(compare ? t("remote.race.racing", { count: variants.length + 1 }) : dispatch ? (orchestrate ? t("board.create.orchestrating") : t("board.create.dispatched")) : t("board.create.savedToBacklog"));
      if (chat) onChat(task);
      if (compare) onCompare?.(task);
    } catch (e) {
      onNotice(String(e), true);
    }
  }
  const cap =
    capInfo === "checking"
      ? t("board.create.checkingCapability")
      : capInfo
        ? t("board.create.capability", {
            profile: capInfo.profile,
            mcp: capInfo.mcp_servers.length ? t("board.create.mcpServers", { servers: capInfo.mcp_servers.join(", ") }) : t("board.create.noMcp"),
            memory: capInfo.memory_dir ? t("board.create.sharedMemory") : t("board.create.noMemory"),
          })
        : "";
  return (
    <Modal id="sheet" open className="sheet" aria-label={t("board.newTask")} onCancel={onClose}>
      <header className="sheet-head">
        <h2>{t("board.newTask")}</h2>
        <button onClick={onClose}>✕</button>
      </header>
      <label>
        {t("board.create.template")}
        <select
          onChange={(e) => {
            const template = templates[Number(e.target.value)];
            if (template) {
              setTitle(template.title ?? title);
              setPrompt(template.prompt ?? prompt);
              setPermission(template.permission_mode ?? permission);
              setModel(template.model ?? model);
            }
          }}
        >
          <option value="">{t("board.create.noTemplate")}</option>
          {templates.map((template, i) => (
            <option value={i} key={template.name}>
              {template.name}
            </option>
          ))}
        </select>
      </label>
      <label>
        {t("board.create.project")}
        <select
          id="f-project"
          value={projectId}
          onChange={(e) => setProjectId(Number(e.target.value))}
        >
          {projects.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name} — {p.target_name}
            </option>
          ))}
        </select>
      </label>
      <div id="f-cap-hint" className="subhint">{cap}<span className="cap-note">{cap.includes("restricted") ? t("board.create.deniedNoPrompt") : ""}</span></div>
      <div id="f-orchestrate" className={"orchestrate-row" + (orchestrate ? " on" : "") + (orchestration?.orchestrate_ready ? "" : " unavailable")}>
        <button
          type="button"
          role="switch"
          aria-checked={orchestrate}
          aria-label={t("board.create.orchestrateLabel")}
          disabled={!orchestration?.orchestrate_ready}
          onClick={() => setOrchestrate(!orchestrate)}
        />
        <span onClick={() => orchestration?.orchestrate_ready && setOrchestrate(!orchestrate)}>
          <b>{t("board.quick.orchestrate")}</b>
          {orchestration?.orchestrate_ready ? (
            <small>
              {t("board.create.orchLeadPlans")}<b>{orchestration.settings?.worker_agent}</b>{t("board.create.orchWorkerBuilds")}<b>{t("board.create.orchLead")}</b>{t("board.create.orchLeadAgents")}
            </small>
          ) : (
            <small>
              {t("board.create.orchNeeds")}<a href="#targets">{t("board.quick.openSettings")}</a>.
            </small>
          )}
        </span>
      </div>
      <label>
        {t("board.create.title")}
        <input
          id="f-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          placeholder={t("board.create.titlePlaceholder")}
        />
      </label>
      <label>
        {orchestrate ? t("board.create.promptOrchestrate") : t("board.create.prompt")}
        <textarea
          id="f-prompt"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder={t("board.create.promptPlaceholder")}
        />
      </label>
      {overlaps.length > 0 && (
        <div className="claims-overlap-warning" id="new-task-claim-overlap" role="status">
          {t("board.create.overlapWarning")}
          <ul>
            {overlaps.map((c) => (
              <li key={c.id}>
                <b>{c.holder}</b>{c.agent ? ` (${c.agent})` : ""} — {claimScopeLabel(c)}
                {c.intent && <> — “{c.intent}”</>}
              </li>
            ))}
          </ul>
          {t("board.create.overlapAdvice")}
        </div>
      )}
      <label>
        {t("board.create.permissions")}
        <select
          id="f-perm"
          value={permission}
          onChange={(e) => setPermission(e.target.value)}
        >
          <option value="default" disabled={agent !== "claude"}>
            {t("board.create.permGated")}
          </option>
          <option value="acceptEdits">
            {t("board.create.permAcceptEdits")}
          </option>
          <option value="plan">{t("board.create.permPlan")}</option>
          <option value="bypassPermissions">
            {t("board.create.permBypass")}
          </option>
        </select>
      </label>
      <fieldset id="f-agent" data-value={agent}>
        <legend>{t("board.create.agent")}</legend>
        {(shownAgents.some((a) => a.name === agent)
          ? shownAgents
          : [...shownAgents, ...moreAgents.filter((a) => a.name === agent)]
        ).map((a) => (
          <button
            type="button"
            className={agent === a.name ? "on" : ""}
            data-agent={a.name}
            key={a.name}
            disabled={orchestrate && a.name !== "claude" && a.name !== "codex"}
            title={orchestrate && a.name !== "claude" && a.name !== "codex" ? t("board.create.leadAgentsOnly") : undefined}
            onClick={() => {
              setAgent(a.name);
              if (a.name !== "claude" && permission === "default")
                setPermission("acceptEdits");
            }}
          >
            {a.name === "claude" ? "Claude Code" : a.name === "codex" ? "Codex" : a.name}
          </button>
        ))}
        {moreAgents.length > 0 && (
          <button type="button" id="f-agent-more" onClick={() => setShowAllAgents(true)}>
            {t("board.create.moreAgents")}
          </button>
        )}
      </fieldset>
      {showAllAgents && (
        <AllAgentsPicker
          agents={moreAgents}
          onPick={(name) => {
            setAgent(name);
            if (name !== "claude" && permission === "default") setPermission("acceptEdits");
          }}
          onClose={() => setShowAllAgents(false)}
        />
      )}
      <label>
        {t("board.create.model")}
        <input
          id="f-model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder={t("board.create.modelDefault")}
        />
      </label>
      <div className="race-row" id="f-race" role="group" aria-label={t("remote.race.group")}>
        <b>{t("remote.race.label")}</b>
        {[2, 3, 4].map((n) => (
          <button
            type="button"
            key={n}
            data-race={n}
            className={race === n ? "on" : ""}
            aria-pressed={race === n}
            disabled={orchestrate}
            onClick={() => (race === n ? (setRace(0), setVariants([])) : applyRace(n))}
          >
            ×{n}
          </button>
        ))}
        <select
          aria-label={t("remote.race.with")}
          value={raceMode}
          disabled={!race}
          onChange={(e) => applyRace(race, e.target.value as RaceMode)}
        >
          <option value="mixed">{t("remote.race.mixed")}</option>
          <option value="same">{t("remote.race.same")}</option>
        </select>
        <span className="subhint">{t("remote.race.hint")}</span>
      </div>
      <div id="f-variants">
        <div className="variants-head">
          <span>
            {t("board.create.attempts")}{variants.length > 0 && ` (${variants.length + 1})`}
          </span>
          <button
            type="button"
            id="f-add-variant"
            disabled={variants.length + 1 >= maxVariants}
            onClick={() =>
              setVariants((v) => [
                ...v,
                {
                  key: ++variantKeySeq,
                  agent: "",
                  model: "",
                  permissionMode: "",
                  launchProfile: "",
                },
              ])
            }
          >
            {t("board.create.addAttempt")}
          </button>
        </div>
        {variants.length > 0 && (
          <p className="subhint">
            {t("board.create.attemptsHint")}
          </p>
        )}
        {variants.map((v, i) => (
          <div className="variant-row" key={v.key}>
            <span className="variant-n">#{i + 2}</span>
            <select
              value={v.launchProfile}
              onChange={(e) => {
                const name = e.target.value;
                setVariants((list) =>
                  list.map((x) =>
                    x.key === v.key ? { ...x, launchProfile: name } : x,
                  ),
                );
              }}
            >
              <option value="">{t("board.create.customAgentModel")}</option>
              {profiles.map((p) => (
                <option key={p.id} value={p.name}>
                  {t("board.create.profileOption", { name: p.name })}
                </option>
              ))}
            </select>
            {!v.launchProfile && (
              <>
                <select
                  value={v.agent}
                  onChange={(e) => {
                    const val = e.target.value;
                    if (val === "__more__") {
                      setShowAllVariantAgents(v.key);
                      return;
                    }
                    setVariants((list) =>
                      list.map((x) =>
                        x.key === v.key ? { ...x, agent: val } : x,
                      ),
                    );
                  }}
                >
                  <option value="">{t("board.create.sameAgent", { agent })}</option>
                  {(v.agent && !shownAgents.some((a) => a.name === v.agent)
                    ? [...shownAgents, ...moreAgents.filter((a) => a.name === v.agent)]
                    : shownAgents
                  ).map((a) => (
                    <option key={a.name} value={a.name}>
                      {a.name}
                    </option>
                  ))}
                  {moreAgents.length > 0 && (
                    <option value="__more__">{t("board.create.moreAgents")}</option>
                  )}
                </select>
                {showAllVariantAgents === v.key && (
                  <AllAgentsPicker
                    agents={moreAgents}
                    onPick={(name) =>
                      setVariants((list) =>
                        list.map((x) =>
                          x.key === v.key ? { ...x, agent: name } : x,
                        ),
                      )
                    }
                    onClose={() => setShowAllVariantAgents(null)}
                  />
                )}
                <input
                  placeholder={t("board.create.modelPlaceholder")}
                  value={v.model}
                  onChange={(e) => {
                    const val = e.target.value;
                    setVariants((list) =>
                      list.map((x) =>
                        x.key === v.key ? { ...x, model: val } : x,
                      ),
                    );
                  }}
                />
              </>
            )}
            <select
              value={v.permissionMode}
              onChange={(e) => {
                const val = e.target.value;
                setVariants((list) =>
                  list.map((x) =>
                    x.key === v.key ? { ...x, permissionMode: val } : x,
                  ),
                );
              }}
            >
              <option value="">{t("board.create.samePermission")}</option>
              <option value="acceptEdits">{t("board.create.variantAcceptEdits")}</option>
              <option value="plan">{t("board.create.variantPlan")}</option>
              <option value="bypassPermissions">{t("board.create.variantBypass")}</option>
            </select>
            <button
              type="button"
              className="variant-remove"
              aria-label={t("board.create.removeAttempt", { n: i + 2 })}
              onClick={() =>
                setVariants((list) => list.filter((x) => x.key !== v.key))
              }
            >
              ✕
            </button>
          </div>
        ))}
      </div>
      <label>
        {t("board.create.priority")}
        <select
          value={priority}
          onChange={(e) => setPriority(Number(e.target.value))}
        >
          <option value={1}>{t("board.create.priorityLow")}</option>
          <option value={2}>{t("board.create.priorityNormal")}</option>
          <option value={3}>{t("board.create.priorityHigh")}</option>
        </select>
      </label>
      <label>
        {t("board.create.budget")}
        <input
          id="f-budget"
          type="number"
          min="0"
          step="0.01"
          value={budget}
          onChange={(e) => setBudget(e.target.value)}
          placeholder={t("board.create.noCap")}
        />
        <span className="subhint">{t("board.create.budgetHint")}</span>
      </label>
      {budgetStatus?.any_blocked && (
        <p id="f-budget-blocked" className="budget-blocked-note">
          {t("board.create.budgetBlocked")}
        </p>
      )}
      <div className="btnrow">
        <button id="f-save" onClick={() => void create(false)}>{t("board.create.saveToBacklog")}</button>
        <button id="f-go" onClick={() => void create(true)}>{t("board.create.dispatchToBoard")}</button>
        {variants.length > 0 && !orchestrate ? (
          <button id="f-race-go" className="ok" onClick={() => void create(true, false, true)}>
            {t("remote.race.go", { count: variants.length + 1 })}
          </button>
        ) : (
          <button id="f-chat" className="ok" onClick={() => void create(true, true)}>
            {t("board.create.dispatchAndChat")}
          </button>
        )}
      </div>
    </Modal>
  );
}
