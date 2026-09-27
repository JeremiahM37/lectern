import { ApiError } from "../api";
import type { SessionView } from "../types";
import type { SessionsApi } from "./Sessions";

// One record GET /sessions/restorable offers: what happened to it, what
// reopening it does, and the last thing said in it.
export interface RestorableSession extends SessionView {
  reason: string;
  reason_label: string;
  action: "resume" | "track" | "relaunch" | "shell" | "handoff" | "history" | "fresh";
  action_label: string;
  note: string;
  // A lost adopted session's conversation matched by folder and time.
  likely_match?: boolean;
  preview: string;
  preview_kind?: "prompt" | "screen";
  superseded_by?: number;
  reopen_url: string;
}

export interface ReopenResult {
  session: SessionView;
  source_id: number;
  action: string;
  message: string;
}

export interface ReopenChoice {
  agent?: string;
  model?: string;
  profile_id?: number;
  name?: string;
}

// NeedsHistory means the record has no bound conversation: the caller should
// open the saved-conversation picker for it instead.
export class NeedsHistory extends Error {}

export async function reopenSession(
  api: SessionsApi,
  id: number,
  choice: ReopenChoice = {},
): Promise<ReopenResult> {
  try {
    return await api.request<ReopenResult>(`/sessions/${id}/reopen`, {
      method: "POST",
      body: { ...choice },
    });
  } catch (error) {
    if (
      error instanceof ApiError &&
      typeof error.payload === "object" &&
      error.payload !== null &&
      "needs_history" in error.payload
    )
      throw new NeedsHistory(error.message);
    throw error;
  }
}

export function closedAge(seconds: number | null | undefined): string {
  if (seconds == null) return "recently";
  const elapsed = Math.max(0, Date.now() / 1000 - seconds);
  if (elapsed < 60) return "just now";
  if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
  if (elapsed < 86400)
    return `${Math.floor(elapsed / 3600)}h ${Math.floor(elapsed / 60) % 60}m ago`;
  return `${Math.floor(elapsed / 86400)}d ${Math.floor(elapsed / 3600) % 24}h ago`;
}
