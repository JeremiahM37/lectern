// Settings → Accounts (docs/accounts.md): extra logins of an agent CLI on a
// machine, which the "swap accounts" usage-limit policy moves limited work
// between. Each account is a private config directory on the machine; the
// page only ever sees its label, whether it is signed in, the limit Lectern
// last saw and the usage the CLI itself last reported.
import { useEffect, useState } from "react";
import type { AgentAccount, Target } from "../types";
import { clock } from "../limits/limit-label";
import "./accounts.css";

export interface AccountsApi {
  request<T>(p: string, o?: { method?: string; body?: unknown }): Promise<T>;
}

const AGENTS = ["claude", "codex", "gemini"];

function usageText(a: AgentAccount): string {
  const u = a.usage;
  if (!u) return "no usage reported yet";
  const parts: string[] = [];
  if (u.rate_5h_pct != null) parts.push(`5h ${u.rate_5h_pct}%`);
  if (u.rate_7d_pct != null) parts.push(`7d ${u.rate_7d_pct}%`);
  return parts.length ? parts.join(" · ") : "no usage reported yet";
}

export function AccountsPanel({
  api,
  targets,
  onNotice,
  onOpenTerminal,
}: {
  api: AccountsApi;
  targets: Target[];
  onNotice(text: string, error?: boolean): void;
  onOpenTerminal(url: string, title: string): void;
}) {
  const [rows, setRows] = useState<AgentAccount[] | null>(null);
  const usable = targets.filter((t) => t.kind !== "sandbox");
  const [targetId, setTargetId] = useState<number>(0);
  const [agent, setAgent] = useState("claude");
  const [label, setLabel] = useState("");
  const [busy, setBusy] = useState(false);

  function load() {
    void api
      .request<AgentAccount[]>("/accounts")
      .then((got) => setRows(got || []))
      .catch((e) => onNotice(String(e), true));
  }
  useEffect(load, []);

  async function add() {
    setBusy(true);
    try {
      await api.request<AgentAccount>("/accounts", {
        method: "POST",
        body: { agent, label: label.trim(), target_id: targetId || usable[0]?.id || 0 },
      });
      setLabel("");
      onNotice(`Added ${agent} account “${label.trim()}”. Sign it in next.`);
      load();
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    } finally {
      setBusy(false);
    }
  }

  async function signIn(a: AgentAccount) {
    try {
      const sess = await api.request<{ id: number }>(`/accounts/${a.id}/login`, { method: "POST" });
      onOpenTerminal(`/terminal/session/${sess.id}`, `Sign in · ${a.label}`);
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    }
  }

  async function remove(a: AgentAccount) {
    if (!window.confirm(`Forget ${a.agent} account “${a.label}”? Its directory and login stay on ${a.target_name}.`)) return;
    try {
      await api.request(`/accounts/${a.id}`, { method: "DELETE" });
      load();
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    }
  }

  const now = Date.now() / 1000;
  return (
    <article id="accounts-panel" className="accounts-panel">
      <h3>Accounts</h3>
      <p className="subhint">
        Several logins of the same agent on a machine. With “swap accounts” on in a usage-limit policy, an agent
        that hits its limit is restarted under the next free account and continues the same conversation. Each
        account is a private directory on the machine that only its CLI reads; Lectern never shows or sends what is
        in it. Only add accounts that are your own, and check that your providers' terms allow how you use them.
      </p>
      {rows === null ? (
        <p>Loading accounts…</p>
      ) : rows.length === 0 ? (
        <p>No accounts yet. The login each agent already uses is added as “Default” with the first one.</p>
      ) : (
        <ul className="accounts-list">
          {rows.map((a) => (
            <li key={a.id} data-account-id={a.id} className="accounts-row">
              <div className="accounts-who">
                <strong>
                  {a.agent} · {a.label}
                </strong>
                {a.default && <span className="subhint"> (its own login)</span>}
                <span className="subhint"> on {a.target_name}</span>
              </div>
              <div className="accounts-state">
                <span>{a.signed_in == null ? "sign-in unknown" : a.signed_in ? "signed in" : "not signed in"}</span>
                <span>{a.blocked_until && a.blocked_until > now ? `limited until ${clock(a.blocked_until, now)}` : "free"}</span>
                <span>{usageText(a)}</span>
                {a.live_sessions > 0 && <span>{a.live_sessions} running</span>}
              </div>
              <div className="accounts-actions">
                <button className="b" onClick={() => void signIn(a)}>
                  Sign in
                </button>
                <button className="b" aria-label={`Remove ${a.label}`} onClick={() => void remove(a)}>
                  Remove
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <fieldset>
        <legend>Add an account</legend>
        <div className="limit-row">
          <label>
            Agent
            <select aria-label="Account agent" value={agent} onChange={(e) => setAgent(e.target.value)}>
              {AGENTS.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <label>
            Machine
            <select
              aria-label="Account machine"
              value={targetId || usable[0]?.id || 0}
              onChange={(e) => setTargetId(Number(e.target.value))}
            >
              {usable.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Label
            <input aria-label="Account label" value={label} maxLength={40} placeholder="work" onChange={(e) => setLabel(e.target.value)} />
          </label>
        </div>
        <button disabled={busy || !label.trim()} onClick={() => void add()}>
          Add account
        </button>
      </fieldset>
    </article>
  );
}
