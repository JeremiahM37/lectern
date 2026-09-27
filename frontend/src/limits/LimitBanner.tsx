// The usage-limit banner on a session card or task (docs/rate-limits.md):
// what stopped the agent, when it resets, and the one-tap choices.
import { useState } from "react";
import type { RequestOptions } from "../api";
import type { LimitHold } from "../types";
import { choiceLabel, limitChoices, limitLabel, type LimitChoice } from "./limit-label";
import "./limits.css";

interface Api {
  request<T>(path: string, options?: RequestOptions): Promise<T>;
}

export function LimitBanner({
  hold,
  api,
  onRefresh,
  onNotice,
  onPickAgent,
}: {
  hold: LimitHold;
  api: Api;
  onRefresh(): Promise<void> | void;
  onNotice(text: string, error?: boolean): void;
  // Opens the agent picker when no fallback is configured; the switch it
  // starts is limit-aware on the server, so no handoff is asked of the
  // stopped agent.
  onPickAgent?(): void;
}) {
  const [busy, setBusy] = useState(false);
  async function choose(choice: LimitChoice) {
    if (choice === "handoff" && !hold.fallback) {
      if (onPickAgent) onPickAgent();
      else onNotice("Set a fallback agent in the project's usage-limit policy to hand off.", true);
      return;
    }
    setBusy(true);
    try {
      const body: Record<string, string | number> = { action: choice };
      if (choice === "swap" && hold.swap_to) body.account_id = hold.swap_to.id;
      await api.request(`/limits/${hold.id}/choose`, { method: "POST", body });
      onNotice(
        choice === "dismiss"
          ? "Limit dismissed."
          : choice === "handoff"
            ? `Handing off to ${hold.fallback}.`
            : choice === "swap"
              ? `Swapping to ${hold.swap_to?.label || "another account"}.`
              : choice === "resume_now"
                ? "Resuming now."
                : "Lectern will resume it after the reset.",
      );
      await onRefresh();
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }
  const choices = limitChoices(hold);
  return (
    <div className="limit-banner" data-limit-id={hold.id} data-limit-state={hold.state}>
      <div className="limit-banner-what">
        <strong>{limitLabel(hold)}</strong>
        <span title={hold.note || undefined}>{hold.message}</span>
      </div>
      {choices.length > 0 && (
        <div className="limit-banner-actions">
          {choices.map((choice) => (
            <button
              key={choice}
              className={`b${choice === "dismiss" ? "" : " ok"}`}
              disabled={busy}
              onClick={() => void choose(choice)}
            >
              {choiceLabel(choice, hold)}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// LimitChip is the compact form for a board card.
export function LimitChip({ hold }: { hold: LimitHold }) {
  return (
    <span className="chip warn limit-chip" title={hold.message}>
      ⏸ {limitLabel(hold)}
    </span>
  );
}
