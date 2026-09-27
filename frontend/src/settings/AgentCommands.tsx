import { useEffect, useState } from "react";
import type { Target } from "../types";
import { Modal } from "../sessions/Modal";
import type { SettingsApi } from "./Settings";
import { t, useLocale } from "../i18n";
interface Command {
  name: string;
  state: "available" | "missing" | "unchecked";
  path?: string;
  detail?: string;
}
export function AgentCommands({
  api,
  target,
  onClose,
}: {
  api: SettingsApi;
  target: Target;
  onClose(): void;
}) {
  useLocale();
  const [rows, setRows] = useState<Command[]>([]),
    [status, setStatus] = useState(() => t("agentSettings.commands.checking"));
  async function load() {
    setStatus(t("agentSettings.commands.checking"));
    try {
      setRows(await api.request<Command[]>(`/targets/${target.id}/agents`));
      setStatus(t("agentSettings.commands.complete"));
    } catch (e) {
      setRows([]);
      setStatus(e instanceof Error ? e.message : String(e));
    }
  }
  useEffect(() => {
    void load();
  }, []);
  return (
    <Modal className="agent-commands" aria-label={t("agentSettings.commands.title")} onCancel={onClose}>
      <h2>{t("agentSettings.commands.title")}</h2>
      <button onClick={onClose}>{t("agentSettings.editor.close")}</button>
      <p>{target.name}</p>
      <p>
        {t("agentSettings.commands.intro")}
      </p>
      <p className="ac-status" role="status">{status}</p>
      <ul className="ac-results">
        {rows.map((r) => (
          <li key={r.name}>
            <b>{r.name}</b>
            <span>
              {r.state === "available"
                ? t("agentSettings.commands.found")
                : r.state === "missing"
                  ? t("agentSettings.commands.notFound")
                  : t("agentSettings.commands.notChecked")}{" "}
              — {r.path || r.detail}
            </span>
          </li>
        ))}
      </ul>
      <button onClick={() => void load()}>{t("agentSettings.commands.checkAgain")}</button>
    </Modal>
  );
}
