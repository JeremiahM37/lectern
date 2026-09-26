// Turns the flat, ordered stream from GET /sessions/{id}/conversation/live
// into the cards the Chat view renders: text/thinking bubbles in order, and
// ONE card per tool call that merges its later tool_result in place (a Bash
// card shows the command AND its output together, not as two separate rows)
// — mirroring how Happy's ToolView carries `result` on the same `tool`
// object rather than as a second message. Pure and DOM-free, unit-tested in
// chatCards.test.ts.

export interface ConversationItem {
  id: string;
  role: string;
  kind: "text" | "thinking" | "tool_use" | "tool_result";
  text?: string;
  tool_name?: string;
  tool_use_id?: string;
  input?: Record<string, unknown>;
  output?: string;
  is_error?: boolean;
  truncated?: boolean;
  timestamp?: string;
}

export interface TextCard {
  kind: "text";
  id: string;
  role: string;
  text: string;
}
export interface ThinkingCard {
  kind: "thinking";
  id: string;
  text: string;
}
export interface ToolCard {
  kind: "tool";
  id: string;
  name: string;
  input: Record<string, unknown>;
  output?: string;
  isError?: boolean;
  outputTruncated?: boolean;
  /** "running" until a correlated tool_result arrives, then "done".
   * "pending" is a third state only ApprovalCard uses — a call that has
   * not even been dispatched yet, so there is no "running" to claim. */
  status: "running" | "done" | "pending";
}
export type ChatCard = TextCard | ThinkingCard | ToolCard;

export function buildChatCards(items: ConversationItem[]): ChatCard[] {
  const cards: ChatCard[] = [];
  const byToolUseId = new Map<string, ToolCard>();
  for (const item of items) {
    switch (item.kind) {
      case "text":
        if (item.text) cards.push({ kind: "text", id: item.id, role: item.role, text: item.text });
        break;
      case "thinking":
        if (item.text) cards.push({ kind: "thinking", id: item.id, text: item.text });
        break;
      case "tool_use": {
        const card: ToolCard = {
          kind: "tool",
          id: item.id,
          name: item.tool_name || "Tool",
          input: item.input || {},
          status: "running",
        };
        cards.push(card);
        if (item.tool_use_id) byToolUseId.set(item.tool_use_id, card);
        break;
      }
      case "tool_result": {
        const existing = item.tool_use_id ? byToolUseId.get(item.tool_use_id) : undefined;
        if (existing) {
          existing.output = item.output;
          existing.isError = item.is_error;
          existing.outputTruncated = item.truncated;
          existing.status = "done";
        } else {
          // A result whose call is outside this page's window (the cursor
          // started after the tool_use) still deserves a card, just an
          // unpaired one, rather than being dropped silently.
          cards.push({
            kind: "tool",
            id: item.id,
            name: "Tool result",
            input: {},
            output: item.output,
            isError: item.is_error,
            outputTruncated: item.truncated,
            status: "done",
          });
        }
        break;
      }
    }
  }
  return cards;
}
