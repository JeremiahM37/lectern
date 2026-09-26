// Form logic for Settings → Budgets → Model prices (GET/PUT /api/model-prices,
// docs/budgets.md "Codex spend is estimated"). Kept apart from the component so
// it can be unit tested.
import type { ModelPrice } from "../types";

export interface SeenModel {
  agent: string;
  model: string;
  tokens: number;
  priced: boolean;
}

// One editable row; rates stay strings while typing.
export interface PriceRow {
  name: string;
  input: string;
  cached: string;
  output: string;
}

export function rowsFromPrices(prices: Record<string, ModelPrice>): PriceRow[] {
  return Object.entries(prices)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, p]) => {
      return {
        name,
        input: String(p.input_per_1m),
        cached:
          p.cached_input_per_1m == null ? "" : String(p.cached_input_per_1m),
        output: String(p.output_per_1m),
      };
    });
}

function rate(value: string): number | undefined {
  if (value.trim() === "") return undefined;
  const n = Number(value);
  return Number.isFinite(n) && n >= 0 ? n : NaN;
}

// validatePrices turns the rows into the PUT body, or explains the first
// problem. Input and output rates are required; cached is optional (unset
// bills cached input at the input rate).
export function validatePrices(rows: PriceRow[]): {
  prices?: Record<string, ModelPrice>;
  error?: string;
} {
  const prices: Record<string, ModelPrice> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (!name) return { error: "Every price needs a model or agent name." };
    if (prices[name]) return { error: `${name} is listed twice.` };
    const input = rate(row.input);
    const output = rate(row.output);
    const cached = rate(row.cached);
    if (input === undefined || output === undefined)
      return { error: `${name}: enter input and output rates.` };
    if (
      Number.isNaN(input) ||
      Number.isNaN(output) ||
      Number.isNaN(cached ?? 0)
    )
      return { error: `${name}: rates must be numbers of 0 or more.` };
    prices[name] = {
      input_per_1m: input,
      output_per_1m: output,
      ...(cached === undefined ? {} : { cached_input_per_1m: cached }),
    };
  }
  return { prices };
}

// unpricedNotice names the seen models no price covers, e.g.
// "Codex spend isn't shown until you set a price for gpt-5-codex."
export function unpricedNotice(seen: SeenModel[]): string {
  const missing = seen.filter((s) => !s.priced);
  if (missing.length === 0) return "";
  const byAgent = new Map<string, string[]>();
  for (const s of missing) {
    const list = byAgent.get(s.agent) ?? [];
    list.push(s.model || s.agent);
    byAgent.set(s.agent, list);
  }
  return [...byAgent.entries()]
    .map(([agent, models]) => {
      const label = agent
        ? agent.charAt(0).toUpperCase() + agent.slice(1)
        : "Token-only";
      return `${label} spend isn't shown until you set a price for ${models.join(", ")}.`;
    })
    .join(" ");
}
