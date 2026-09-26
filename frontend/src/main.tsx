import {flushSync} from "react-dom";
import { createRoot } from "react-dom/client";
import App from "./App";
import Pair from "./pairing/Pair";
import "../../web/static/style.css";
import "../../web/static/conversation.css";
import "../../web/static/workspace.css";
import "../../web/static/launch-profiles.css";
import "../../web/static/agent-settings.css";
import "../../web/static/command-palette.css";
import "./shell/shell.css";
import "./settings/connect-tools.css";
import "./settings/devices.css";
import "./pairing/pair.css";
// Last, so a phone's density overrides every view's desktop sizing.
import "./shell/mobile.css";
// /pair is the one page an unpaired device can reach with no credential —
// see internal/api/server.go's withAuth exemption. It gets its own render
// root entirely: no board fetches, no SSE, nothing that assumes an
// authenticated session before the exchange has even happened.
const root = createRoot(document.getElementById("root")!);
flushSync(() =>
  root.render(window.location.pathname === "/pair" ? <Pair /> : <App />),
);
