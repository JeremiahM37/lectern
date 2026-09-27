// The usage-limit policy editor (docs/rate-limits.md): what Lectern does when
// an agent is stopped by its provider's usage limit — for every project
// (global) or one project. Sessions can be given their own through the API.
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { LimitPolicy } from "../types";
import { t, useLocale } from "../i18n";

interface Api {
  request<T>(p: string, o?: { method?: string; body?: JsonValue }): Promise<T>;
}

interface PolicyResponse {
  own: LimitPolicy | null;
  effective: LimitPolicy;
  effective_scope: string;
}

type Mode = Exclude<LimitPolicy["mode"], "swap">;

const MODES = (): [Mode, string][] => [
  ["notify", t("agentSettings.limits.mode.notify")],
  ["wait", t("agentSettings.limits.mode.wait")],
  ["handoff", t("agentSettings.limits.mode.handoff")],
];

// The short lower-case name of a policy mode, as shown in "Use the default (…)".
function modeName(mode: string): string {
  switch (mode) {
    case "notify":
      return t("agentSettings.limits.modeName.notify");
    case "wait":
      return t("agentSettings.limits.modeName.wait");
    case "handoff":
      return t("agentSettings.limits.modeName.handoff");
    case "swap":
      return t("agentSettings.limits.modeName.swap");
    default:
      return mode;
  }
}

export function LimitPolicyEditor({
  api,
  projectId,
  onNotice,
}: {
  api: Api;
  projectId?: number;
  onNotice(text: string, error?: boolean): void;
}) {
  useLocale();
  const query = projectId ? `?project_id=${projectId}` : "";
  const [loaded, setLoaded] = useState<PolicyResponse | null>(null);
  const [mode, setMode] = useState<"" | Mode>(projectId ? "" : "notify");
  // swap moves a limited agent to another signed-in account of the same CLI
  // first (docs/accounts.md); mode is then what happens when none is free.
  const [swap, setSwap] = useState(false);
  const [agent, setAgent] = useState("");
  const [model, setModel] = useState("");
  const [agents, setAgents] = useState<string[]>([]);
  const [status, setStatus] = useState("");
  useEffect(() => {
    api
      .request<PolicyResponse>(`/limits/policy${query}`)
      .then((got) => {
        setLoaded(got);
        const p = got.own;
        setSwap(p?.mode === "swap");
        setMode(p ? (p.mode === "swap" ? p.then || "notify" : p.mode) : projectId ? "" : "notify");
        setAgent(p?.fallback_agent || "");
        setModel(p?.fallback_model || "");
      })
      .catch(() => undefined);
    api
      .request<{ name: string }[]>("/agents")
      .then((rows) => setAgents((rows || []).map((row) => row.name).filter((name) => name !== "shell")))
      .catch(() => undefined);
  }, [query]);
  async function save() {
    const body: Record<string, JsonValue> = {
      policy:
        mode === ""
          ? null
          : swap
            ? { mode: "swap", then: mode, fallback_agent: agent, fallback_model: model }
            : { mode, fallback_agent: agent, fallback_model: model },
    };
    if (projectId) body.project_id = projectId;
    try {
      const got = await api.request<PolicyResponse>("/limits/policy", { method: "PUT", body });
      setLoaded(got);
      setStatus(t("agentSettings.limits.savedStatus"));
      onNotice(t("agentSettings.limits.saved"));
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error));
    }
  }
  const inherited = loaded && !loaded.own ? loaded.effective : null;
  return (
    <section className="limit-policy" data-setting="budgets.limits">
      <h4>{t("agentSettings.limits.title")}</h4>
      {mode !== "" && (
        <label className="limit-policy-swap">
          <input type="checkbox" aria-label={t("agentSettings.limits.swapAccounts")} checked={swap} onChange={(e) => setSwap(e.target.checked)} />
          {t("agentSettings.limits.swapHint")}
        </label>
      )}
      <label>
        {swap && mode !== "" ? t("agentSettings.limits.whenAllLimited") : t("agentSettings.limits.policy")}
        <select aria-label={t("agentSettings.limits.policyLabel")} value={mode} onChange={(e) => setMode(e.target.value as typeof mode)}>
          {projectId && (
            <option value="">
              {inherited
                ? t("agentSettings.limits.useDefaultInherited", {
                    policy: inherited.mode === "swap" ? t("agentSettings.limits.swapThen", { then: modeName(String(inherited.then)) }) : modeName(inherited.mode),
                  })
                : t("agentSettings.limits.useDefault")}
            </option>
          )}
          {MODES().map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </label>
      {mode !== "" && (
        <>
          <label>
            {mode === "handoff" ? t("agentSettings.limits.fallbackAgent") : t("agentSettings.limits.fallbackAgentForHandoff")}
            <select aria-label={t("agentSettings.limits.fallbackAgent")} value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="">{t("agentSettings.limits.none")}</option>
              {agents.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <label>
            {t("agentSettings.limits.fallbackModel")}
            <input aria-label={t("agentSettings.limits.fallbackModel")} value={model} placeholder={t("agentSettings.limits.agentDefault")} onChange={(e) => setModel(e.target.value)} />
          </label>
        </>
      )}
      <button onClick={() => void save()}>{t("agentSettings.limits.save")}</button>
      <p className="limit-policy-status" role="status">{status}</p>
    </section>
  );
}
