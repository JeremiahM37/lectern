// The usage-limit policy editor (docs/rate-limits.md): what Lectern does when
// an agent is stopped by its provider's usage limit — for every project
// (global) or one project. Sessions can be given their own through the API.
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { LimitPolicy } from "../types";

interface Api {
  request<T>(p: string, o?: { method?: string; body?: JsonValue }): Promise<T>;
}

interface PolicyResponse {
  own: LimitPolicy | null;
  effective: LimitPolicy;
  effective_scope: string;
}

type Mode = Exclude<LimitPolicy["mode"], "swap">;

const MODES: [Mode, string][] = [
  ["notify", "Notify me with one-tap choices"],
  ["wait", "Wait, then resume the same agent at the reset"],
  ["handoff", "Hand off now to another agent"],
];

export function LimitPolicyEditor({
  api,
  projectId,
  onNotice,
}: {
  api: Api;
  projectId?: number;
  onNotice(text: string, error?: boolean): void;
}) {
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
      setStatus("Saved usage-limit policy");
      onNotice("Usage-limit policy saved");
    } catch (error) {
      setStatus(error instanceof Error ? error.message : String(error));
    }
  }
  const inherited = loaded && !loaded.own ? loaded.effective : null;
  return (
    <section className="limit-policy">
      <h4>When an agent hits its usage limit</h4>
      {mode !== "" && (
        <label className="limit-policy-swap">
          <input type="checkbox" aria-label="Swap accounts" checked={swap} onChange={(e) => setSwap(e.target.checked)} />
          First swap to another signed-in account of the same agent and continue the conversation (Settings →
          Accounts)
        </label>
      )}
      <label>
        {swap && mode !== "" ? "When every account is limited" : "Policy"}
        <select aria-label="Usage-limit policy" value={mode} onChange={(e) => setMode(e.target.value as typeof mode)}>
          {projectId && (
            <option value="">
              Use the default{inherited ? ` (${inherited.mode === "swap" ? `swap, then ${inherited.then}` : inherited.mode})` : ""}
            </option>
          )}
          {MODES.map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </label>
      {mode !== "" && (
        <>
          <label>
            Fallback agent{mode === "handoff" ? "" : " (for “Hand off”)"}
            <select aria-label="Fallback agent" value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="">none</option>
              {agents.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <label>
            Fallback model
            <input aria-label="Fallback model" value={model} placeholder="agent default" onChange={(e) => setModel(e.target.value)} />
          </label>
        </>
      )}
      <button onClick={() => void save()}>Save usage-limit policy</button>
      <p className="limit-policy-status" role="status">{status}</p>
    </section>
  );
}
