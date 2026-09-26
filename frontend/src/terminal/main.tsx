// First, so a relayed terminal's requests use the tunnel (relay/boot.ts).
import "../relay/boot";
import {createClient} from "../api/client";
import { errorMessage } from "./model";
import { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { TerminalApp, type SharedTool } from "./App";
import { Review } from "./Review";
import { Dialog } from "./dialogs";
import { json, type TerminalInfo } from "./model";
import { NativeHistory, NativeSearch } from "../sessions/SavedConversations";
import type { SessionsApi } from "../sessions/Sessions";
import type { SessionView, Target } from "../types";
import "./native-history.css";
import "./native-search.css";
const api: SessionsApi = {
  sessions: (options) =>
    json<SessionView[]>(
      "/api/sessions" +
        (options?.all
          ? "?all=true"
          : options?.archived
            ? "?archived=true"
            : ""),
      { signal: options?.signal },
    ),
  request: createClient(),
};
type ToolState =
  | { tool: "review"; info: TerminalInfo }
  | { tool: "saved"; session: SessionView }
  | { tool: "search"; targets: Target[] }
  | { tool: "message"; message: string }
  | undefined;
function Page() {
  const parts = location.pathname.split("/"),
    kind = parts[2] || "session",
    id = parts[3] || "1";
  const [state, setState] = useState<ToolState>();
  const [message, setMessage] = useState("");
  const revision = useRef(0);
  useEffect(
    () => () => {
      revision.current++;
    },
    [],
  );
  const close = () => {
    revision.current++;
    setState(undefined);
  };
  async function open(tool: SharedTool, info: TerminalInfo) {
    const current = ++revision.current;
    try {
      if (tool === "review") {
        setState({ tool, info });
        return;
      }
      setState({ tool: "message", message: "Loading…" });
      if (tool === "saved") {
        const session = await json<SessionView>(
          "/api/sessions/" + encodeURIComponent(id),
        );
        if (current === revision.current) setState({ tool, session });
      } else {
        const targets = await json<Target[]>("/api/targets");
        if (current === revision.current) setState({ tool, targets });
      }
    } catch (error) {
      if (current === revision.current)
        setState({ tool: "message", message: errorMessage(error) });
    }
  }
  const notice = (text: string) => setMessage(text);
  return (
    <>
      <TerminalApp
        kind={kind}
        id={id}
        externalNotice={message}
        onShared={(tool, info) => {
          void open(tool, info);
        }}
      />
      {state?.tool === "review" && (
        <Review kind={kind} id={id} name={state.info.workdir} onClose={close} />
      )}
      {state?.tool === "saved" && (
        <NativeHistory
          api={api}
          session={state.session}
          onClose={close}
          onSession={() =>
            notice(
              "Fork started. Find it in Sessions; this terminal stays attached.",
            )
          }
          onNotice={notice}
        />
      )}
      {state?.tool === "search" && (
        <NativeSearch
          api={api}
          targets={state.targets}
          onClose={close}
          onFork={() =>
            notice(
              "Fork started. Find it in Sessions; this terminal stays attached.",
            )
          }
          onNotice={notice}
        />
      )}
      {state?.tool === "message" && (
        <Dialog id="terminal-message" title="Terminal" onClose={close}>
          <p role="status">{state.message}</p>
        </Dialog>
      )}
    </>
  );
}
createRoot(document.getElementById("root")!).render(<Page />);
