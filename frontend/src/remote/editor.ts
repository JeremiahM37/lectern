// "Open in editor" (docs/ssh.md): a link that opens a workspace in the
// editor on the computer you are using. For a remote machine it uses the
// editor's own SSH remote support, so the editor connects to the machine
// directly; for this server's own files it is a plain file link, which only
// works when the browser runs on this server — set an editor host to open
// them over SSH instead.
import type { Target } from "../types";

export type EditorId = "vscode" | "cursor" | "windsurf" | "zed";
export const EDITORS: { id: EditorId | "none"; label: string }[] = [
  { id: "vscode", label: "VS Code" },
  { id: "cursor", label: "Cursor" },
  { id: "windsurf", label: "Windsurf" },
  { id: "zed", label: "Zed" },
  { id: "none", label: "Don't show" },
];
export const EDITOR_PREF = "editor.app";

export interface SSHOptions {
  alias?: string;
  proxy_jump?: string;
  forward_agent?: boolean;
  no_agent?: boolean;
  identity_agent?: string;
  options?: string[];
  transport?: "" | "builtin" | "openssh";
  editor_host?: string;
}

export function sshOptions(target: Pick<Target, "ssh_json">): SSHOptions {
  try {
    const parsed: unknown = JSON.parse(target.ssh_json || "{}");
    return parsed && typeof parsed === "object" ? (parsed as SSHOptions) : {};
  } catch {
    return {};
  }
}

// editorHost is the SSH destination your own computer would use, or "" for
// a file on this computer, or null when there is no way to reach it.
export function editorHost(target: Pick<Target, "kind" | "host" | "user" | "port" | "ssh_json">): string | null {
  const opts = sshOptions(target);
  if (opts.editor_host) return opts.editor_host;
  if (target.kind === "local") return "";
  if (target.kind !== "ssh" || !target.host) return null;
  if (opts.alias) return opts.alias;
  const user = target.user ? `${target.user}@` : "";
  return `${user}${target.host}${target.port && target.port !== 22 ? `:${target.port}` : ""}`;
}

export function editorLink(
  editor: EditorId,
  target: Pick<Target, "kind" | "host" | "user" | "port" | "ssh_json">,
  path: string,
): string | null {
  if (!path.startsWith("/")) return null;
  const host = editorHost(target);
  if (host === null) return null;
  const encodedPath = path.split("/").map(encodeURIComponent).join("/");
  if (host === "") return editor === "zed" ? `zed://file${encodedPath}` : `${editor}://file${encodedPath}`;
  if (editor === "zed") return `zed://ssh/${host}${encodedPath}`;
  // The VS Code family names the SSH host in the authority. A port has to
  // come from the host's ssh config, so it is dropped here.
  const bare = host.replace(/:\d+$/, "");
  return `${editor}://vscode-remote/ssh-remote+${encodeURIComponent(bare)}${encodedPath}`;
}
