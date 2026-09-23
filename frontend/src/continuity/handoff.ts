import { useEffect, useState } from 'react';
import type { RequestOptions } from '../api';
import type { SessionView } from '../types';

// Continuity reads a few session fields that are not part of the shared card
// contract yet. Keeping the extension local means the owner of types.ts can add
// them on its own schedule without the two definitions racing.
export type ContinuitySession = SessionView & {
  handoff_phase?: string;
  handoff_destination?: string;
  handoff_error?: string;
  predecessor_id?: number | null;
  successor_id?: number | null;
};

// The minimum of the API surface these components need, so they can be dropped
// into Sessions/Conversation without importing the whole app.
export type SessionApi = {
  request<T>(path: string, options?: RequestOptions): Promise<T>;
};

export type SwitchRequest = {
  agent: string;
  model: string;
  profile: number;
  destination: string;
};

export type SwitchPhase = 'saving' | 'starting' | 'ready' | 'failed';

export type SwitchProgress = {
  phase: SwitchPhase;
  destination: string;
  successor?: { id: number; name: string };
  error?: string;
};

type WrapRow = { id: number; next_session_id: number | null };

const AGENT_LABELS: Record<string, string> = { claude: 'Claude', codex: 'Codex', gemini: 'Gemini' };

export const agentLabel = (name?: string) => (name && AGENT_LABELS[name]) || name || 'Session';

export const agentTitle = (name?: string) =>
  name ? name.charAt(0).toUpperCase() + name.slice(1) : 'Session';

export const describeSwitch = (agent: string, model: string, profileName?: string) =>
  profileName ||
  (model ? `${agentTitle(agent)} · ${model}` : `${agentTitle(agent)} · default model`);

export const requestSwitch = (api: SessionApi, sessionID: number, request: SwitchRequest) =>
  api.request<{ after_wrap_id: number }>(`/sessions/${sessionID}/handoff`, {
    method: 'POST',
    body: {
      successor: true,
      kill_old: false,
      quick_switch: true,
      agent: request.agent,
      model: request.model,
      profile_id: request.profile,
    },
  });

// useHandoffProgress watches one switch from the API. "Ready" only ever means a
// successor session exists: while the backend is still writing the wrap or
// launching the destination, the phase stays saving/starting.
export function useHandoffProgress(
  api: SessionApi,
  sessionID: number | null,
  afterWrap: number,
  generation: number,
  active: boolean,
  destination: string,
): SwitchProgress {
  const [progress, setProgress] = useState<SwitchProgress>({
    phase: 'saving',
    destination,
  });
  useEffect(() => {
    if (!active || sessionID == null) return;
    setProgress({ phase: 'saving', destination });
    let stopped = false;
    let timer: number | undefined;
    const tick = async () => {
      try {
        const [view, wraps] = await Promise.all([
          api.request<ContinuitySession>(`/sessions/${sessionID}`),
          api.request<WrapRow[]>(`/sessions/${sessionID}/wraps`),
        ]);
        if (stopped) return;
        const label = view.handoff_destination || destination;
        const started = wraps.find((wrap) => wrap.id > afterWrap);
        const successor = wraps.find((wrap) => wrap.id > afterWrap && wrap.next_session_id);
        if (successor?.next_session_id) {
          let name = '';
          try {
            name = (await api.request<ContinuitySession>(`/sessions/${successor.next_session_id}`)).name;
          } catch {
            name = '';
          }
          if (stopped) return;
          setProgress({
            phase: 'ready',
            destination: label,
            successor: { id: successor.next_session_id, name },
          });
          return;
        }
        if (view.handoff_in_flight) {
          setProgress({
            phase: view.handoff_phase === 'starting' || started ? 'starting' : 'saving',
            destination: label,
          });
        } else if (view.handoff_error) {
          setProgress({ phase: 'failed', destination: label, error: view.handoff_error });
          return;
        } else if (started) {
          setProgress({
            phase: 'failed',
            destination: label,
            error:
              'The context was saved, but the new session did not start. Your original session is still available.',
          });
          return;
        } else {
          setProgress({
            phase: 'failed',
            destination: label,
            error: 'The switch stopped before a new session started. Your original session is still available.',
          });
          return;
        }
      } catch {
        // A dropped request is not a failed switch; keep watching.
      }
      if (!stopped) timer = window.setTimeout(tick, 1000);
    };
    void tick();
    return () => {
      stopped = true;
      if (timer) window.clearTimeout(timer);
    };
  }, [api, sessionID, afterWrap, generation, active, destination]);
  return progress;
}
