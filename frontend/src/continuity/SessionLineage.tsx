import { useEffect, useState } from 'react';
import { agentTitle, type ContinuitySession, type SessionApi } from './handoff';
import './continuity.css';

export const lineageLabel = (session: ContinuitySession) =>
  session.launch_profile || session.model || agentTitle(session.agent);

// SessionLineage is the navigable thread of a switched conversation: the session
// that handed work over, and the one it handed to. It is deliberately a plain
// component with an `api` prop so Sessions and Conversation can mount it too.
export function SessionLineage({
  api,
  session,
  onOpen,
  onOpenChat,
  pending,
  className,
}: {
  api: SessionApi;
  session: ContinuitySession;
  onOpen?(session: ContinuitySession): void;
  onOpenChat?(session: ContinuitySession): void;
  pending?: boolean;
  className?: string;
}) {
  const predecessorID = session.predecessor_id || undefined;
  const successorID = session.successor_id || undefined;
  const [neighbors, setNeighbors] = useState<{
    predecessor?: ContinuitySession;
    successor?: ContinuitySession;
  }>({});
  useEffect(() => {
    let stopped = false;
    const ids = [predecessorID, successorID].filter((id): id is number => !!id);
    if (!ids.length) {
      setNeighbors({});
      return;
    }
    void Promise.all(
      ids.map((id) =>
        api.request<ContinuitySession>(`/sessions/${id}`).catch(() => undefined),
      ),
    ).then((rows) => {
      if (stopped) return;
      const byID = new Map(
        rows.filter((row): row is ContinuitySession => !!row).map((row) => [row.id, row]),
      );
      setNeighbors({
        predecessor: predecessorID ? byID.get(predecessorID) : undefined,
        successor: successorID ? byID.get(successorID) : undefined,
      });
    });
    return () => {
      stopped = true;
    };
  }, [api, predecessorID, successorID]);

  const predecessor = neighbors.predecessor;
  const successor = neighbors.successor;
  if (!predecessor && !successor && !pending) return null;
  const entry = (row: ContinuitySession, back: boolean) => (
    <span className="lineage-entry">
      <button
        className="b lineage-open"
        aria-label={back ? `Go back to ${row.name}` : `Open ${row.name}`}
        onClick={() => onOpen?.(row)}
      >
        {back && <span aria-hidden="true">←</span>}
        <strong>{row.name || `Session ${row.id}`}</strong>
        <small>
          {lineageLabel(row)}
          {row.ended_at ? ' · ended' : ''}
        </small>
        {!back && <span aria-hidden="true">→</span>}
      </button>
      {onOpenChat && (
        <button
          className="b lineage-chat"
          aria-label={`Open chat for ${row.name || `session ${row.id}`}`}
          onClick={() => onOpenChat(row)}
        >
          Chat
        </button>
      )}
    </span>
  );
  return (
    <nav className={`lineage ${className || ''}`.trim()} aria-label="Conversation lineage">
      {predecessor && entry(predecessor, true)}
      {pending && !successor && (
        <span className="lineage-pending" role="status">
          Saving context…
        </span>
      )}
      {successor && entry(successor, false)}
    </nav>
  );
}
