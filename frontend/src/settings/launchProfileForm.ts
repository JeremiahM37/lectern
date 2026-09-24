import type { JsonValue } from "../api";

// The saved shape returned by GET/POST/PUT /api/launch-profiles. Description and
// instructions are optional so the UI keeps working against a server that has
// not yet shipped the richer profile fields.
export type LaunchProfileRecord = {
  id: number;
  name: string;
  agent: string;
  command: string;
  model: string;
  env_json: string;
  description?: string;
  instructions?: string;
};

// GET /api/launch-profile-presets returns a ready-made profile keyed by a
// stable slug. Presets are drafts only: they are never written until the user
// saves them.
export type StarterPreset = {
  key: string;
  name: string;
  agent: string;
  description: string;
  instructions: string;
};

// An unsaved draft carries id 0 until it is stored under a server id.
export type LaunchProfileDraft = {
  id: number;
  name: string;
  agent: string;
  command: string;
  model: string;
  env_json: string;
  description: string;
  instructions: string;
};

export const INSTRUCTIONS_HELP =
  "Sent to the agent when a session starts. Resumes and forks keep the original briefing. " +
  "Team workflows use the chosen agent's available delegation tools.";

export function formattedEnvironment(env_json?: string): string {
  try {
    return JSON.stringify(JSON.parse(env_json || "{}"), null, 2);
  } catch {
    return "{}";
  }
}

export function draftFromProfile(
  profile: LaunchProfileRecord | undefined,
  fallbackAgent: string,
): LaunchProfileDraft {
  return {
    id: profile?.id ?? 0,
    name: profile?.name ?? "",
    agent: profile?.agent || fallbackAgent,
    command: profile?.command ?? "",
    model: profile?.model ?? "",
    env_json: formattedEnvironment(profile?.env_json),
    description: profile?.description ?? "",
    instructions: profile?.instructions ?? "",
  };
}

// Turns a starter preset into an editable, unsaved draft. It intentionally
// carries no server id, so applying a starter can never overwrite the profile
// that is currently selected.
export function draftFromPreset(
  preset: StarterPreset,
  fallbackAgent: string,
): LaunchProfileDraft {
  return {
    id: 0,
    name: preset.name,
    agent: preset.agent || fallbackAgent,
    command: "",
    model: "",
    env_json: "{}",
    description: preset.description ?? "",
    instructions: preset.instructions ?? "",
  };
}

export function parseEnvironment(
  value: string,
): { env?: Record<string, string>; error?: string } {
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    return { error: "Environment must be valid JSON." };
  }
  if (
    !parsed ||
    Array.isArray(parsed) ||
    typeof parsed !== "object" ||
    Object.values(parsed).some((v) => typeof v !== "string")
  )
    return { error: "Environment must be a JSON object with string values." };
  return { env: parsed as Record<string, string> };
}

export function profileRequestBody(
  draft: LaunchProfileDraft,
  env: Record<string, string>,
): JsonValue {
  return {
    name: draft.name.trim(),
    agent: draft.agent,
    command: draft.command.trim(),
    model: draft.model.trim(),
    env_json: JSON.stringify(env),
    description: draft.description.trim(),
    instructions: draft.instructions.trim(),
  };
}
