import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
import { formatAgo } from "./ConnectTools";
import { withToken } from "../api";
import { t, useLocale } from "../i18n";
// backups.css is imported by main.tsx, like devices.css.

// Settings → Backups (internal/backup, internal/api/backup.go).
export interface BackupStatus {
  running: boolean;
  last_at?: string;
  last_ok_at?: string;
  error?: string;
  tag?: string;
  url?: string;
  bytes?: number;
  sessions?: number;
  skipped?: string[];
}

export interface BackupState {
  repo: string;
  keep: number;
  every_hours: number;
  has_passphrase: boolean;
  github: string;
  status: BackupStatus;
}

export function humanSize(n = 0): string {
  if (n >= 1 << 30) return `${(n / (1 << 30)).toFixed(1)} GB`;
  if (n >= 1 << 20) return `${Math.round(n / (1 << 20))} MB`;
  return `${Math.max(1, Math.round(n / 1024))} KB`;
}

const seconds = (iso?: string) => (iso ? Date.parse(iso) / 1000 : 0);

export function Backups({ api, onNotice }: { api: SettingsApi; onNotice(t: string, e?: boolean): void }) {
  useLocale();
  const [state, setState] = useState<BackupState>();
  const [repo, setRepo] = useState("");
  const [every, setEvery] = useState("24");
  const [keep, setKeep] = useState("14");
  const [passphrase, setPassphrase] = useState<string>();
  const [saving, setSaving] = useState(false);

  function apply(s: BackupState) {
    setState(s);
    setRepo(s.repo);
    setEvery(String(s.every_hours));
    setKeep(String(s.keep));
  }
  function load() {
    void api.request<BackupState>("/backup").then(apply).catch((error) => onNotice(String(error), true));
  }
  useEffect(load, []);
  // While one runs, watch it finish.
  useEffect(() => {
    if (!state?.status.running) return;
    const id = window.setInterval(() => {
      void api.request<BackupState>("/backup").then((s) => {
        setState(s);
        if (!s.status.running) onNotice(s.status.error ? t("settings.backup.failed", { error: s.status.error }) : t("settings.backup.done"), !!s.status.error);
      });
    }, 3000);
    return () => window.clearInterval(id);
  }, [state?.status.running]);

  async function save(extra: Record<string, boolean> = {}) {
    const hours = Number(every), count = Number(keep);
    if (!Number.isInteger(hours) || hours < 0 || !Number.isInteger(count) || count < 1) {
      onNotice(t("settings.backup.invalid"), true);
      return;
    }
    setSaving(true);
    try {
      const out = await api.request<{ backup: BackupState; passphrase?: string; created?: boolean; repo_error?: string }>("/backup", {
        method: "PUT",
        body: { repo: repo.trim(), every_hours: hours, keep: count, create_repo: !!repo.trim(), ...extra },
      });
      apply(out.backup);
      if (out.passphrase) setPassphrase(out.passphrase);
      if (out.repo_error) onNotice(out.repo_error, true);
      else onNotice(out.created ? t("settings.backup.created", { repo: out.backup.repo }) : t("settings.backup.saved"));
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setSaving(false);
    }
  }

  async function runNow() {
    try {
      setState(await api.request<BackupState>("/backup/run", { method: "POST" }));
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  if (!state) return null;
  const st = state.status;
  const ready = !!state.repo && state.has_passphrase && !!state.github;
  return (
    <article className="backup-panel">
      <h3 data-setting="backup.repo">{t("settings.section.backups")}</h3>
      <p className="subhint">{t("settings.backup.intro")}</p>
      {!state.github && <p className="subhint backup-warn" role="alert">{t("settings.backup.noGitHub")}</p>}

      <p className="backup-status" data-testid="backup-status">
        {st.running
          ? t("settings.backup.running")
          : st.last_ok_at
            ? t("settings.backup.last", { ago: formatAgo(seconds(st.last_ok_at)), size: humanSize(st.bytes), sessions: st.sessions ?? 0 })
            : t("settings.backup.never")}
        {!st.running && st.url && (
          <>
            {" · "}
            <a href={st.url} target="_blank" rel="noreferrer">{st.tag}</a>
          </>
        )}
      </p>
      {!st.running && st.error && (
        <p className="subhint backup-warn" role="alert">
          {t("settings.backup.lastFailed", { ago: formatAgo(seconds(st.last_at)), error: st.error })}
        </p>
      )}

      <label className="backup-field">
        {t("settings.backup.repo")}
        <input value={repo} placeholder="you/lectern-backups" autoCapitalize="off" autoCorrect="off" spellCheck={false} onChange={(e) => setRepo(e.target.value)} />
      </label>
      <div className="backup-row">
        <label className="backup-field" data-setting="backup.schedule">
          {t("settings.backup.every")}
          <input type="number" min={0} value={every} onChange={(e) => setEvery(e.target.value)} />
        </label>
        <label className="backup-field">
          {t("settings.backup.keep")}
          <input type="number" min={1} value={keep} onChange={(e) => setKeep(e.target.value)} />
        </label>
      </div>
      <p className="subhint">{t("settings.backup.scheduleHint")}</p>

      {passphrase ? (
        <div className="backup-passphrase" role="alert">
          <strong>{t("settings.backup.saveThis")}</strong>
          <code>{passphrase}</code>
          <button className="b" onClick={() => void navigator.clipboard?.writeText(passphrase).then(() => onNotice(t("settings.backup.copied")))}>
            {t("settings.backup.copy")}
          </button>
        </div>
      ) : (
        !state.has_passphrase && <p className="subhint">{t("settings.backup.needPassphrase")}</p>
      )}

      <div className="backup-actions">
        <button className="b" disabled={saving} onClick={() => void save(state.has_passphrase ? {} : { new_passphrase: true })}>
          {state.has_passphrase ? t("settings.backup.save") : t("settings.backup.setUp")}
        </button>
        <button className="b ok" disabled={!ready || st.running} onClick={() => void runNow()}>
          {t("settings.backup.now")}
        </button>
        {state.has_passphrase && (
          <a className="b" href={withToken("/api/backup/download")} download>
            {t("settings.backup.download")}
          </a>
        )}
      </div>
      <p className="subhint">
        {t("settings.backup.restoreBefore")} <code>lectern backup restore --repo {state.repo || "you/lectern-backups"}</code>
        {t("settings.backup.restoreAfter")}
      </p>
    </article>
  );
}
