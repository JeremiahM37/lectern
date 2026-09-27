// "Open in VS Code / Cursor / Windsurf / Zed" for a workspace on any machine
// (docs/ssh.md). The editor preference follows the person across devices;
// Settings → Machines → Files & editor sets it.
import { useEffect, useState } from "react";
import { createClient } from "../api";
import type { Target } from "../types";
import { EditorLink } from "./MachineRemote";

let targets: Promise<Target[]> | undefined;
function loadTargets() {
  targets ??= createClient()<Target[]>("/targets").catch(() => {
    targets = undefined;
    return [];
  });
  return targets;
}

export function OpenInEditor({ targetId, path }: { targetId: number | undefined; path: string | undefined }) {
  const [target, setTarget] = useState<Target>();
  useEffect(() => {
    if (!targetId || !path) return;
    let alive = true;
    void loadTargets().then((ts) => alive && setTarget(ts.find((t) => t.id === targetId)));
    return () => {
      alive = false;
    };
  }, [targetId, path]);
  if (!target || !path) return null;
  return <EditorLink target={target} path={path} compact />;
}
