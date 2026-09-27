// Settings → Machines → "Import from ~/.ssh/config" (docs/ssh.md): every
// concrete Host in the Lectern server's SSH config, resolved the way ssh
// resolves it, becomes a machine in one click.
import { useEffect, useState } from "react";
import { Modal } from "../sessions/Modal";
import type { Target } from "../types";
import type { RemoteApi } from "./MachineRemote";
import "./remote.css";

interface Host {
  alias: string;
  hostname: string;
  user: string;
  port: number;
  identity_files?: string[];
  proxy_jump?: string;
  proxy_command?: boolean;
  forward_agent?: boolean;
  gssapi?: boolean;
  security_key?: boolean;
  resolver: string;
  target_id?: number;
  target_name?: string;
}

export function SshImport({ api, onClose, onImported, onNotice }: {
  api: RemoteApi; onClose(): void; onImported(): void; onNotice(t: string, e?: boolean): void;
}) {
  const [data, setData] = useState<{ hosts: Host[]; path: string; openssh?: boolean; error?: string }>();
  const [picked, setPicked] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    void api
      .request<{ hosts: Host[]; path: string; openssh?: boolean; error?: string }>("/ssh/hosts")
      .then((d) => {
        setData(d);
        setPicked(new Set(d.hosts.filter((h) => !h.target_id).map((h) => h.alias)));
      })
      .catch((e) => onNotice(String(e), true));
  }, []);
  const fresh = data?.hosts.filter((h) => !h.target_id) || [];
  return (
    <Modal className="sheet ssh-import" aria-label="Import SSH hosts" onCancel={onClose}>
      <div className="sheet-head">
        <h2>Import SSH hosts</h2>
        <button type="button" onClick={onClose} aria-label="Close">×</button>
      </div>
      {!data && <p>Reading the SSH config…</p>}
      {data && (
        <>
          <p className="sub">
            From <code>{data.path}</code> on the Lectern server.
            {data.openssh ? " Imported machines connect by alias through OpenSSH, so everything the entry says applies." : " OpenSSH is not installed here, so the built-in client uses the resolved host, user, port and key."}
          </p>
          {data.error && <p role="alert">{data.error}</p>}
          <ul className="ssh-hosts">
            {data.hosts.map((h) => (
              <li key={h.alias} data-alias={h.alias}>
                <label>
                  <input
                    type="checkbox"
                    disabled={!!h.target_id}
                    checked={picked.has(h.alias)}
                    onChange={(e) =>
                      setPicked((old) => {
                        const next = new Set(old);
                        if (e.target.checked) next.add(h.alias);
                        else next.delete(h.alias);
                        return next;
                      })
                    }
                  />
                  <b>{h.alias}</b>
                  <span className="sub">
                    {h.user ? `${h.user}@` : ""}{h.hostname}{h.port && h.port !== 22 ? `:${h.port}` : ""}
                  </span>
                </label>
                <span className="ssh-tags">
                  {h.proxy_jump && <span className="chip">via {h.proxy_jump}</span>}
                  {h.proxy_command && <span className="chip">ProxyCommand</span>}
                  {h.forward_agent && <span className="chip">agent forwarding</span>}
                  {h.gssapi && <span className="chip">Kerberos</span>}
                  {h.security_key && <span className="chip info">security key</span>}
                  {h.target_id && <span className="chip ds">added as {h.target_name}</span>}
                </span>
              </li>
            ))}
            {data.hosts.length === 0 && <li className="sub">No Host entries with a plain name were found.</li>}
          </ul>
          <div className="btnrow">
            {fresh.length > 1 && (
              <button type="button" onClick={() => setPicked(new Set(fresh.map((h) => h.alias)))}>Select all</button>
            )}
            <button
              type="button"
              className="ok"
              id="ssh-import-go"
              disabled={busy || picked.size === 0}
              onClick={() => {
                setBusy(true);
                void api
                  .request<{ results: { alias: string; target?: Target; error?: string }[] }>("/ssh/import", {
                    method: "POST",
                    body: { aliases: [...picked] },
                  })
                  .then((r) => {
                    const ok = r.results.filter((x) => x.target).length;
                    const failed = r.results.filter((x) => x.error);
                    onNotice(
                      `Imported ${ok} machine${ok === 1 ? "" : "s"}` +
                        (failed.length ? ` · ${failed.map((f) => `${f.alias}: ${f.error}`).join("; ")}` : ""),
                      failed.length > 0 && ok === 0,
                    );
                    onImported();
                    onClose();
                  })
                  .catch((e) => onNotice(String(e), true))
                  .finally(() => setBusy(false));
              }}
            >
              {busy ? "Importing…" : `Import ${picked.size || ""} host${picked.size === 1 ? "" : "s"}`}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}
