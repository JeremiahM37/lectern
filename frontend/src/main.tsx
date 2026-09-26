import {flushSync} from "react-dom";
import { createRoot } from "react-dom/client";
import App from "./App";
import "../../web/static/style.css";
import "../../web/static/conversation.css";
import "../../web/static/workspace.css";
import "../../web/static/launch-profiles.css";
import "../../web/static/agent-settings.css";
import "../../web/static/command-palette.css";
import "./shell/shell.css";
import "./settings/connect-tools.css";
// Last, so a phone's density overrides every view's desktop sizing.
import "./shell/mobile.css";
flushSync(()=>createRoot(document.getElementById("root")!).render(<App />));
