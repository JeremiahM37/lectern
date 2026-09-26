// Settings → Budgets → Model prices: the $/1M-token table Lectern uses to
// estimate spend for agents that report tokens but no dollars (Codex). Rows
// for models seen in usage without a price are highlighted so the gap is
// obvious. Backed by GET/PUT /api/model-prices.
import { useEffect, useState } from "react";
import type { JsonValue } from "../api";
import type { ModelPrice } from "../types";
import {
  rowsFromPrices,
  unpricedNotice,
  validatePrices,
  type PriceRow,
  type SeenModel,
} from "./modelPrices";

interface Api {
  request<T>(p: string, o?: { method?: string; body?: JsonValue }): Promise<T>;
}

interface PricesResponse {
  prices: Record<string, ModelPrice>;
  seen: SeenModel[];
}

export function ModelPrices({
  api,
  onNotice,
}: {
  api: Api;
  onNotice(t: string, e?: boolean): void;
}) {
  const [rows, setRows] = useState<PriceRow[]>();
  const [seen, setSeen] = useState<SeenModel[]>([]);

  function apply(r: PricesResponse) {
    setRows(rowsFromPrices(r.prices || {}));
    setSeen(r.seen || []);
  }
  useEffect(() => {
    void api
      .request<PricesResponse>("/model-prices")
      .then(apply)
      .catch((e) => onNotice(String(e), true));
  }, []);

  if (!rows) {
    return (
      <article id="model-prices" className="budgets-editor">
        <h3>Model prices</h3>
        <p>Loading prices…</p>
      </article>
    );
  }
  const { prices, error } = validatePrices(rows);
  const notice = unpricedNotice(seen);
  const listed = new Set(rows.map((r) => r.name.trim()));
  const unpriced = seen.filter(
    (s) => !s.priced && !listed.has(s.model || s.agent),
  );
  const update = (i: number, field: keyof PriceRow, value: string) =>
    setRows((list) =>
      list?.map((row, j) => (j === i ? { ...row, [field]: value } : row)),
    );

  function save() {
    if (!prices) return;
    void api
      .request<PricesResponse>("/model-prices", {
        method: "PUT",
        body: { prices } as unknown as JsonValue,
      })
      .then((r) => {
        apply(r);
        onNotice("Model prices saved");
      })
      .catch((e) => onNotice(String(e), true));
  }

  return (
    <article id="model-prices" className="budgets-editor">
      <h3>Model prices</h3>
      <p className="subhint">
        USD per 1M tokens, used to estimate spend for agents that report tokens
        but no cost (Codex). A row named after an agent (e.g. <code>codex</code>
        ) covers its models without their own row. Cached input is optional;
        left blank it is billed at the input rate.
      </p>
      {notice && (
        <p
          className="budget-blocked-note"
          id="model-prices-unpriced"
          role="status"
        >
          {notice}
        </p>
      )}
      <div className="mp-grid" role="table" aria-label="Model prices">
        <div className="mp-row mp-head" role="row">
          <span role="columnheader">Model or agent</span>
          <span role="columnheader">Input</span>
          <span role="columnheader">Cached input</span>
          <span role="columnheader">Output</span>
          <span />
        </div>
        {rows.map((row, i) => (
          <div className="mp-row" role="row" key={i}>
            <input
              type="text"
              aria-label="Model or agent"
              placeholder="model or agent"
              value={row.name}
              onChange={(e) => update(i, "name", e.target.value)}
            />
            {(["input", "cached", "output"] as const).map((field) => (
              <input
                key={field}
                aria-label={`${row.name || "new"} ${field} per 1M`}
                type="number"
                min="0"
                step="0.001"
                inputMode="decimal"
                placeholder={`${field} $`}
                value={row[field]}
                onChange={(e) => update(i, field, e.target.value)}
              />
            ))}
            <button
              className="b"
              aria-label={`Remove ${row.name}`}
              onClick={() => setRows((list) => list?.filter((_, j) => j !== i))}
            >
              ✕
            </button>
          </div>
        ))}
        {unpriced.map((s) => (
          <div
            className="mp-row model-price-missing"
            role="row"
            key={`${s.agent}/${s.model}`}
          >
            <span className="mp-name">
              <b>{s.model || s.agent}</b>{" "}
              <span className="sub">
                ({s.agent}, no price · {s.tokens.toLocaleString()} tokens / 30
                days)
              </span>
            </span>
            <button
              className="b mp-set"
              onClick={() =>
                setRows((list) => [
                  ...(list ?? []),
                  {
                    name: s.model || s.agent,
                    input: "",
                    cached: "",
                    output: "",
                  },
                ])
              }
            >
              Set price
            </button>
          </div>
        ))}
      </div>
      {error && rows.length > 0 && (
        <p className="budget-blocked-note" role="alert">
          {error}
        </p>
      )}
      <div className="btnrow">
        <button
          className="b"
          onClick={() =>
            setRows((list) => [
              ...(list ?? []),
              { name: "", input: "", cached: "", output: "" },
            ])
          }
        >
          + Add price
        </button>
        <button id="model-prices-save" disabled={!prices} onClick={save}>
          Save prices
        </button>
      </div>
    </article>
  );
}
