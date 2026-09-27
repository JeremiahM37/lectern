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
import { t, useLocale } from "../i18n";

interface Api {
  request<T>(p: string, o?: { method?: string; body?: JsonValue }): Promise<T>;
}

// The lower-case rate name inside a row's field label and placeholder.
function fieldName(field: "input" | "cached" | "output"): string {
  return field === "input"
    ? t("agentSettings.prices.field.input")
    : field === "cached"
      ? t("agentSettings.prices.field.cached")
      : t("agentSettings.prices.field.output");
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
  useLocale();
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
      <article id="model-prices" className="budgets-editor" data-setting="budgets.prices">
        <h3>{t("agentSettings.prices.title")}</h3>
        <p>{t("agentSettings.prices.loading")}</p>
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
        onNotice(t("agentSettings.prices.saved"));
      })
      .catch((e) => onNotice(String(e), true));
  }

  return (
    <article id="model-prices" className="budgets-editor" data-setting="budgets.prices">
      <h3>{t("agentSettings.prices.title")}</h3>
      <p className="subhint">
        {t("agentSettings.prices.introBefore")}{" "}<code>codex</code>
        {t("agentSettings.prices.introAfter")}
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
      <div className="mp-grid" role="table" aria-label={t("agentSettings.prices.title")}>
        <div className="mp-row mp-head" role="row">
          <span role="columnheader">{t("agentSettings.prices.modelOrAgent")}</span>
          <span role="columnheader">{t("agentSettings.prices.input")}</span>
          <span role="columnheader">{t("agentSettings.prices.cachedInput")}</span>
          <span role="columnheader">{t("agentSettings.prices.output")}</span>
          <span />
        </div>
        {rows.map((row, i) => (
          <div className="mp-row" role="row" key={i}>
            <input
              type="text"
              aria-label={t("agentSettings.prices.modelOrAgent")}
              placeholder={t("agentSettings.prices.modelOrAgentPlaceholder")}
              value={row.name}
              onChange={(e) => update(i, "name", e.target.value)}
            />
            {(["input", "cached", "output"] as const).map((field) => (
              <input
                key={field}
                aria-label={t("agentSettings.prices.rateLabel", { name: row.name || t("agentSettings.prices.newRow"), field: fieldName(field) })}
                type="number"
                min="0"
                step="0.001"
                inputMode="decimal"
                placeholder={t("agentSettings.prices.ratePlaceholder", { field: fieldName(field) })}
                value={row[field]}
                onChange={(e) => update(i, field, e.target.value)}
              />
            ))}
            <button
              className="b"
              aria-label={t("agentSettings.prices.remove", { name: row.name })}
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
                {t("agentSettings.prices.unpricedRow", { agent: s.agent, tokens: s.tokens.toLocaleString() })}
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
              {t("agentSettings.prices.setPrice")}
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
          {t("agentSettings.prices.addPrice")}
        </button>
        <button id="model-prices-save" disabled={!prices} onClick={save}>
          {t("agentSettings.prices.save")}
        </button>
      </div>
    </article>
  );
}
