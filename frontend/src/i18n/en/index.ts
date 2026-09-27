// English, the source catalog: complete by definition. Each area of the app
// keeps its strings in its own file so they can be edited side by side; this
// file joins them. A key defined twice is a bug (i18n.test.ts checks).
import core from "./core";
import app from "./app";
import settings from "./settings";
import settingsAgents from "./settings-agents";
import sessions from "./sessions";
import conversation from "./conversation";
import review from "./review";
import board from "./board";
import terminal from "./terminal";
import browser from "./browser";
import trackers from "./trackers";
import files from "./files";
import remote from "./remote";
import plugins from "./plugins";

export const areas: Record<string, Record<string, string>> = { core, app, settings, "settings-agents": settingsAgents, sessions, conversation, review, board, terminal, browser, trackers, files, remote, plugins };
const en: Record<string, string> = Object.assign({}, ...Object.values(areas));
export default en;
