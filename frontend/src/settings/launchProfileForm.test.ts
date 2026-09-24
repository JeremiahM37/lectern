import assert from "node:assert/strict";
import { test } from "node:test";
import {
  draftFromPreset,
  draftFromProfile,
  formattedEnvironment,
  parseEnvironment,
  profileRequestBody,
  type StarterPreset,
} from "./launchProfileForm";

const preset: StarterPreset = {
  key: "lean-builder",
  name: "Lean Builder",
  agent: "codex",
  description: "Small, reviewable changes.",
  instructions: "Keep the diff tight and explain each step.",
};

test("a starter preset becomes an unsaved draft that cannot overwrite a saved profile", () => {
  const draft = draftFromPreset(preset, "claude");
  assert.equal(draft.id, 0);
  assert.equal(draft.name, "Lean Builder");
  assert.equal(draft.agent, "codex");
  assert.equal(draft.command, "");
  assert.equal(draft.model, "");
  assert.equal(draft.env_json, "{}");
  assert.equal(draft.description, preset.description);
  assert.equal(draft.instructions, preset.instructions);
});

test("a preset without an agent falls back to the first runner", () => {
  assert.equal(draftFromPreset({ ...preset, agent: "" }, "claude").agent, "claude");
});

test("selecting a saved profile restores its description, instructions and environment", () => {
  const draft = draftFromProfile(
    {
      id: 7,
      name: "Reviewed Delivery",
      agent: "claude",
      command: "flag",
      model: "sonnet",
      env_json: '{"API_KEY":"kept"}',
      description: "Every change gets a review pass.",
      instructions: "Review the diff before you answer.",
    },
    "codex",
  );
  assert.equal(draft.id, 7);
  assert.equal(draft.agent, "claude");
  assert.equal(draft.env_json, formattedEnvironment('{"API_KEY":"kept"}'));
  assert.equal(draft.description, "Every change gets a review pass.");
  assert.equal(draft.instructions, "Review the diff before you answer.");
});

test("environment parsing keeps the existing validation contract", () => {
  assert.deepEqual(parseEnvironment('{"API_KEY":"kept"}'), { env: { API_KEY: "kept" } });
  assert.equal(parseEnvironment("not json").error, "Environment must be valid JSON.");
  assert.equal(parseEnvironment("[]").error, "Environment must be a JSON object with string values.");
  assert.equal(parseEnvironment('{"n":1}').error, "Environment must be a JSON object with string values.");
});

test("the saved payload trims text fields and encodes the environment object", () => {
  const draft = draftFromProfile(undefined, "claude");
  assert.deepEqual(profileRequestBody({ ...draft, name: "  Spaced  ", description: " why ", instructions: " how " }, { A: "1" }), {
    name: "Spaced",
    agent: "claude",
    command: "",
    model: "",
    env_json: '{"A":"1"}',
    description: "why",
    instructions: "how",
  });
});

