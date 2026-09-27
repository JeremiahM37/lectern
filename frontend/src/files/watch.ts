// Live workspace state: a long-poll on the target (inotify on Linux, a quick
// scan elsewhere; see workspace_files.py do_watch) says when a shown folder or
// git's state changed. If the server cannot watch (an older server, too many
// watches, an error), the page falls back to polling every few seconds.
import { useEffect, useRef, useState } from "react";
import type { FileApi } from "./api";

export type WatchMode = "inotify" | "poll" | "fallback" | "idle";

export function useFileWatch(api: FileApi, dirs: string[], enabled: boolean, onChange: () => void): WatchMode {
  const [mode, setMode] = useState<WatchMode>("idle");
  const latest = useRef(onChange);
  latest.current = onChange;
  const key = [...new Set(dirs)].sort().join("\n");
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    const list = key.split("\n").filter(Boolean);
    let token = "",
      stopped = false,
      failures = 0;
    const sleep = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));
    const visible = () =>
      document.hidden
        ? new Promise<void>((resolve) => {
            const shown = () => {
              if (document.hidden) return;
              document.removeEventListener("visibilitychange", shown);
              resolve();
            };
            document.addEventListener("visibilitychange", shown);
          })
        : Promise.resolve();
    void (async () => {
      while (!stopped) {
        await visible();
        if (stopped) return;
        try {
          const result = await api.watch(list.length ? list : ["."], token, 25, controller.signal);
          failures = 0;
          setMode(result.mode);
          if (result.changed) latest.current();
          token = result.token;
        } catch {
          if (stopped || controller.signal.aborted) return;
          // Polling fallback: refresh on a timer, and try watching again later.
          failures++;
          setMode("fallback");
          for (let i = 0; i < (failures > 3 ? 8 : 2) && !stopped; i++) {
            await sleep(4000);
            if (!document.hidden) latest.current();
          }
          token = "";
        }
      }
    })();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [api, key, enabled]);
  return mode;
}
