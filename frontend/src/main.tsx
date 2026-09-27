// First: when this device is paired over the encrypted relay, every request
// to this Lectern must go through the tunnel from the very first one.
import { relayTunnel } from "./relay/boot";
import {flushSync} from "react-dom";
import { createRoot } from "react-dom/client";
import App from "./App";
import Pair from "./pairing/Pair";
import RelayPair from "./relay/RelayPair";
import { RelayBanner } from "./relay/RelayBanner";
import "./relay/relay.css";
import "../../web/static/style.css";
import "../../web/static/conversation.css";
import "../../web/static/workspace.css";
import "../../web/static/launch-profiles.css";
import "../../web/static/agent-settings.css";
import "../../web/static/command-palette.css";
import "./shell/shell.css";
import "./settings/connect-tools.css";
import "./settings/agent-catalog.css";
import "./settings/devices.css";
import "./settings/model-prices.css";
import "./pairing/pair.css";
// Last, so a phone's density overrides every view's desktop sizing.
import "./shell/mobile.css";
// /pair is the one page an unpaired device can reach with no credential —
// see internal/api/server.go's withAuth exemption. It gets its own render
// root entirely: no board fetches, no SSE, nothing that assumes an
// authenticated session before the exchange has even happened.
const root = createRoot(document.getElementById("root")!);
const path = window.location.pathname;
// The Android app's unseen page for notification buttons (native/action.ts).
if (path === "/native-action") void import("./native/action").then((m) => m.runNativeAction());
else flushSync(() =>
  root.render(path === "/pair" ? <Pair /> : path === "/relay-pair" ? <RelayPair /> : <><RelayBanner tunnel={relayTunnel} /><App /></>),
);
