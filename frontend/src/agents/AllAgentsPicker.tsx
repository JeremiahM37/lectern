import { Modal } from "../sessions/Modal";

export interface PickableAgent {
  name: string;
  builtin?: boolean;
}

// AllAgentsPicker is the "More agents…" destination every picker in this
// app opens instead of growing its own dropdown/fieldset into a long
// scroll: one small list of every registered agent (shown ones included, so
// this always works as a complete picker on its own), with capability-
// unaware selection — the caller decides what an unshown/less-common agent
// can actually do here.
export function AllAgentsPicker({
  agents,
  onPick,
  onClose,
  title = "All agents",
}: {
  agents: PickableAgent[];
  onPick(name: string): void;
  onClose(): void;
  title?: string;
}) {
  return (
    <Modal
      className="all-agents-picker"
      aria-label={title}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <h2>{title}</h2>
      <button onClick={onClose}>Close</button>
      <ul className="all-agents-list">
        {agents.map((a) => (
          <li key={a.name}>
            <button
              type="button"
              onClick={() => {
                onPick(a.name);
                onClose();
              }}
            >
              {a.name}
              {a.builtin ? " · built in" : ""}
            </button>
          </li>
        ))}
      </ul>
      {agents.length === 0 && <p>No agents are registered yet.</p>}
    </Modal>
  );
}
