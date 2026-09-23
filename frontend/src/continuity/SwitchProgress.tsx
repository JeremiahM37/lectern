import { useEffect, useRef } from 'react';
import {
  useHandoffProgress,
  type SessionApi,
  type SwitchProgress as Progress,
} from './handoff';
import './continuity.css';

export type PendingSwitch = {
  after: number;
  // generation changes only when a handoff request has been accepted, so a
  // retry that is rejected cannot silently reset the progress surface.
  generation: number;
  destination: string;
  agent: string;
  model: string;
  profile: number;
  error?: string;
  // successor is set once a successor session exists. While it is set the only
  // recovery is opening that session — never asking for another handoff.
  successor?: { id: number; name: string };
};

const stepLabels = (progress: Progress) => [
  'Saving context',
  progress.destination ? `Starting ${progress.destination}` : 'Starting the new session',
  'Ready',
];

// SwitchSteps is the honest three-beat progress the operator asked for. It is
// exported so the picker and the persistent banner show identical wording.
export function SwitchSteps({ progress }: { progress: Progress }) {
  const labels = stepLabels(progress);
  const reached = progress.phase === 'ready' ? 3 : progress.phase === 'starting' ? 1 : 0;
  return (
    <ol className="switch-steps" role="status" aria-live="polite">
      {labels.map((label, index) => {
        const state =
          progress.phase === 'failed'
            ? 'todo'
            : index < reached
              ? 'done'
              : index === reached
                ? 'active'
                : 'todo';
        return (
          <li key={label} data-state={state}>
            <span className="switch-step-dot" aria-hidden="true" />
            {label}
          </li>
        );
      })}
    </ol>
  );
}

// SwitchProgressPanel survives a reload: the app stores which sessions have a
// switch pending, and this surface re-reads the phase from the API when it
// mounts.
export function SwitchProgressPanel({
  api,
  pending,
  onReady,
  onFailed,
  onRetry,
  onReopen,
  onDismiss,
}: {
  api: SessionApi;
  pending: Record<string, PendingSwitch>;
  onReady(source: number, successor: { id: number; name: string }): void;
  onFailed(source: number, error: string): void;
  onRetry(source: number): void;
  onReopen(source: number): void;
  onDismiss(source: number): void;
}) {
  const entries = Object.entries(pending);
  if (!entries.length) return null;
  return (
    <div className="switch-progress-panel">
      {entries.map(([id, row]) => (
        <SwitchProgressRow
          key={`${id}-${row.generation}`}
          api={api}
          source={Number(id)}
          pending={row}
          onReady={onReady}
          onFailed={onFailed}
          onRetry={onRetry}
          onReopen={onReopen}
          onDismiss={onDismiss}
        />
      ))}
    </div>
  );
}

function SwitchProgressRow({
  api,
  source,
  pending,
  onReady,
  onFailed,
  onRetry,
  onReopen,
  onDismiss,
}: {
  api: SessionApi;
  source: number;
  pending: PendingSwitch;
  onReady(source: number, successor: { id: number; name: string }): void;
  onFailed(source: number, error: string): void;
  onRetry(source: number): void;
  onReopen(source: number): void;
  onDismiss(source: number): void;
}) {
  const progress = useHandoffProgress(api, source, pending.after, pending.generation, true, pending.destination);
  const reported = useRef('');
  useEffect(() => {
    const stamp = `${pending.generation}:${progress.phase}:${progress.successor?.id ?? ''}:${progress.error ?? ''}`;
    if (reported.current === stamp) return;
    if (progress.phase === 'ready' && progress.successor) {
      reported.current = stamp;
      onReady(source, progress.successor);
    } else if (progress.phase === 'failed') {
      reported.current = stamp;
      onFailed(source, progress.error || 'The switch did not complete. Your original session is still available.');
    }
  }, [pending.generation, progress, source, onReady, onFailed]);
  // A successor that exists but could not be opened is not a failed switch: the
  // session is real, the operator retries opening it.
  const blocked = !!pending.successor && !!pending.error;
  const failed = progress.phase === 'failed' || blocked;
  const failure =
    pending.error || progress.error || 'The switch did not complete. Your original session is still available.';
  return (
    <section
      className={`switch-progress ${failed ? 'failed' : ''}`}
      aria-label={`Switch session ${source} progress`}
    >
      <div className="switch-progress-head">
        <strong>
          {blocked
            ? 'New session started — could not open it'
            : failed
              ? 'Switch failed'
              : `Switching${pending.destination ? ` to ${pending.destination}` : ''}`}
        </strong>
        {failed && (
          <button
            className="switch-progress-x"
            aria-label="Dismiss switch progress"
            onClick={() => onDismiss(source)}
          >
            ✕
          </button>
        )}
      </div>
      <SwitchSteps progress={progress} />
      {failed && (
        <div className="switch-progress-actions">
          <p role="alert">{failure}</p>
          {pending.successor ? (
            <button className="b" onClick={() => onReopen(source)}>
              Open new session
            </button>
          ) : (
            <button className="b" onClick={() => onRetry(source)}>
              Retry
            </button>
          )}
        </div>
      )}
    </section>
  );
}
