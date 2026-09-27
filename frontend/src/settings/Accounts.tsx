// Settings → Accounts (docs/accounts.md): extra logins of an agent CLI on a
// machine, which the "swap accounts" usage-limit policy moves limited work
// between. Each account is a private config directory on the machine; the
// page only ever sees its label, whether it is signed in, the limit Lectern
// last saw and the usage the CLI itself last reported. accounts.css is
// imported by main.tsx, like the other settings styles, so tests can load this.
import { useEffect, useState } from "react";
import type { AgentAccount, Target } from "../types";
import { clock } from "../limits/limit-label";
import { t, useLocale } from "../i18n";

export interface AccountsApi {
  request<T>(p: string, o?: { method?: string; body?: unknown }): Promise<T>;
}

const AGENTS = ["claude", "codex", "gemini"];

function usageText(a: AgentAccount): string {
  const u = a.usage;
  if (!u) return t("settings.accounts.noUsage");
  const parts: string[] = [];
  if (u.rate_5h_pct != null) parts.push(t("settings.accounts.usage5h", { pct: u.rate_5h_pct }));
  if (u.rate_7d_pct != null) parts.push(t("settings.accounts.usage7d", { pct: u.rate_7d_pct }));
  return parts.length ? parts.join(" · ") : t("settings.accounts.noUsage");
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
  useLocale();
  const [rows, setRows] = useState<AgentAccount[] | null>(null);
  const usable = targets.filter((target) => target.kind !== "sandbox");
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
      onNotice(t("settings.accounts.added", { agent, label: label.trim() }));
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
      onOpenTerminal(`/terminal/session/${sess.id}`, t("settings.accounts.signInTitle", { label: a.label }));
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    }
  }

  async function remove(a: AgentAccount) {
    if (!window.confirm(t("settings.accounts.forgetConfirm", { agent: a.agent, label: a.label, target: a.target_name }))) return;
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
      <h3>{t("settings.section.accounts")}</h3>
      <p className="subhint">
        {t("settings.accounts.hint")}
      </p>
      {rows === null ? (
        <p>{t("settings.accounts.loading")}</p>
      ) : rows.length === 0 ? (
        <p>{t("settings.accounts.empty")}</p>
      ) : (
        <ul className="accounts-list">
          {rows.map((a) => (
            <li key={a.id} data-account-id={a.id} className="accounts-row">
              <div className="accounts-who">
                <strong>
                  {a.agent} · {a.label}
                </strong>
                {a.default && <span className="subhint"> {t("settings.accounts.ownLogin")}</span>}
                <span className="subhint"> {t("settings.accounts.onTarget", { target: a.target_name })}</span>
              </div>
              <div className="accounts-state">
                <span>{a.signed_in == null ? t("settings.accounts.signInUnknown") : a.signed_in ? t("settings.accounts.signedIn") : t("settings.accounts.notSignedIn")}</span>
                <span>{a.blocked_until && a.blocked_until > now ? t("settings.accounts.limitedUntil", { time: clock(a.blocked_until, now) }) : t("settings.accounts.free")}</span>
                <span>{usageText(a)}</span>
                {a.live_sessions > 0 && <span>{t("settings.accounts.running", { n: a.live_sessions })}</span>}
              </div>
              <div className="accounts-actions">
                <button className="b" onClick={() => void signIn(a)}>
                  {t("settings.accounts.signIn")}
                </button>
                <button className="b" aria-label={t("settings.remove", { name: a.label })} onClick={() => void remove(a)}>
                  {t("settings.common.remove")}
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <fieldset>
        <legend>{t("settings.accounts.add")}</legend>
        <div className="limit-row">
          <label>
            {t("settings.accounts.agent")}
            <select aria-label={t("settings.accounts.agentAria")} value={agent} onChange={(e) => setAgent(e.target.value)}>
              {AGENTS.map((name) => (
                <option key={name}>{name}</option>
              ))}
            </select>
          </label>
          <label>
            {t("settings.accounts.machine")}
            <select
              aria-label={t("settings.accounts.machineAria")}
              value={targetId || usable[0]?.id || 0}
              onChange={(e) => setTargetId(Number(e.target.value))}
            >
              {usable.map((target) => (
                <option key={target.id} value={target.id}>
                  {target.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("settings.accounts.label")}
            <input aria-label={t("settings.accounts.labelAria")} value={label} maxLength={40} placeholder={t("settings.accounts.labelPlaceholder")} onChange={(e) => setLabel(e.target.value)} />
          </label>
        </div>
        <button disabled={busy || !label.trim()} onClick={() => void add()}>
          {t("settings.accounts.addButton")}
        </button>
      </fieldset>
    </article>
  );
}
