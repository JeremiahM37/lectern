// fr: one file per area of the app, with exactly the English keys
// (i18n.test.ts checks). This file joins them.
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
import simple from "./simple";

export const areas: Record<string, Record<string, string>> = { core, app, settings, "settings-agents": settingsAgents, sessions, conversation, review, board, terminal, browser, trackers, files, remote, plugins, simple };
const catalog: Record<string, string> = Object.assign({}, ...Object.values(areas));
export default catalog;
