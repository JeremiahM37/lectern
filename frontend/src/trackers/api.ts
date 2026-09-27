import type { JsonValue } from "../api";
import { isForge } from "./logic";
import type { ItemRef } from "./types";

// The slice of the app's api client the Tasks hub needs, so it can be
// driven by the real client or a test harness alike.
export interface TrackerApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue; signal?: AbortSignal }): Promise<T>;
}

export type Notice = (text: string, error?: boolean) => void;

/** Where an issue's detail lives: the project's forge, or a connection. */
export function issuePath(projectId: number, ref: ItemRef): string {
  if (isForge(ref.source))
    return `/projects/${projectId}/forge/issues/${encodeURIComponent(ref.id)}`;
  return `/trackers/${ref.connection_id}/issues/${encodeURIComponent(ref.id)}`;
}

export function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
