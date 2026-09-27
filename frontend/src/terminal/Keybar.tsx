// The phone's key row (keybar.ts) and the dialog that arranges it. Holding
// an arrow or Backspace repeats it, like a hardware key; a drag along the
// row scrolls it without pressing anything.
import { useEffect, useRef, useState } from "react";
import { Dialog } from "./dialogs";
import "./keybar.css";
import type { Mods } from "./keys";
import type { Snippet } from "./snippets";
import { haptic } from "../mobile/haptics";
import {
  addItem,
  builtinKeys,
  builtinLabels,
  defaultKeybar,
  itemBytes,
  itemId,
  itemLabel,
  missingBuiltins,
  move,
  repeats,
  snippetItem,
  validComboKey,
  type BuiltinKey,
  type KeyItem,
} from "./keybar";

const REPEAT_DELAY = 380,
  REPEAT_EVERY = 55;

export function Keybar({
  row,
  disabled,
  mods,
  appCursor,
  onMod,
  onSend,
  onSnippets,
  onEdit,
}: {
  row: KeyItem[];
  disabled: boolean;
  mods: Mods;
  appCursor: () => boolean;
  onMod: (id: "ctrl" | "alt") => void;
  onSend: (bytes: string) => void;
  onSnippets: () => void;
  onEdit: () => void;
}) {
  const hold = useRef<{ id: number; x: number; timer?: number; interval?: number; fired: boolean }>(undefined);
  const stop = () => {
    const h = hold.current;
    if (!h) return;
    clearTimeout(h.timer);
    clearInterval(h.interval);
  };
  useEffect(() => stop, []);
  const armed = mods.ctrl || mods.alt;
  return (
    <div id="terminal-keybar" role="group" aria-label="Terminal keys">
      {row.map((item) => {
        const id = itemId(item),
          [label, spoken] = itemLabel(item);
        const send = () => onSend(itemBytes(item, !armed && appCursor()));
        return (
          <button
            key={id}
            data-terminal-key={id}
            className={item.t === "mod" ? "modifier" : item.t === "snippets" ? "snippets" : item.t === "text" ? "quick" : undefined}
            aria-label={spoken}
            title={item.t === "snippets" ? "Saved replies" : undefined}
            aria-pressed={item.t === "mod" ? mods[item.id] : undefined}
            disabled={disabled}
            // Keep the phone keyboard as it is: pressing a key must not take
            // focus away from the terminal, or open the keyboard if hidden.
            onPointerDown={(event) => {
              event.preventDefault();
              stop();
              if (!repeats(item)) return;
              const h = { id: event.pointerId, x: event.clientX, fired: false } as NonNullable<typeof hold.current>;
              hold.current = h;
              h.timer = window.setTimeout(() => {
                h.fired = true;
                haptic("tick");
                send();
                h.interval = window.setInterval(send, REPEAT_EVERY);
              }, REPEAT_DELAY);
            }}
            onPointerMove={(event) => {
              // A drag along the row is a scroll, not a held key.
              const h = hold.current;
              if (h && h.id === event.pointerId && Math.abs(event.clientX - h.x) > 10) stop();
            }}
            onPointerUp={stop}
            onPointerCancel={stop}
            onPointerLeave={stop}
            onContextMenu={(event) => event.preventDefault()}
            onClick={() => {
              const h = hold.current;
              hold.current = undefined;
              if (h?.fired) return; // the hold already sent it, repeatedly
              if (item.t === "mod") onMod(item.id);
              else if (item.t === "snippets") onSnippets();
              else send();
            }}
          >
            {label}
          </button>
        );
      })}
      <button data-terminal-key="edit" className="keybar-edit" aria-label="Customize keys" title="Customize keys"
        onPointerDown={(event) => event.preventDefault()} onClick={onEdit}>
        ✎
      </button>
    </div>
  );
}

const comboKeys: BuiltinKey[] = ["left", "right", "up", "down", "home", "end", "pageup", "pagedown", "tab", "enter", "backspace", "delete", "escape"];

export function KeybarEditor({
  row,
  snippets,
  onChange,
  onClose,
}: {
  row: KeyItem[];
  snippets: Snippet[];
  onChange: (row: KeyItem[] | null) => void;
  onClose: () => void;
}) {
  const [combo, setCombo] = useState({ key: "r", ctrl: true, alt: false, shift: false });
  const [custom, setCustom] = useState("");
  const comboKey = combo.key === "custom" ? custom : combo.key;
  const comboItem: KeyItem = { t: "combo", key: comboKey, ctrl: combo.ctrl, alt: combo.alt, shift: combo.shift };
  const comboOk = validComboKey(comboKey) && (combo.ctrl || combo.alt || combo.shift);
  const present = new Set(row.map(itemId));
  return (
    <Dialog id="keybar-dialog" title="Customize keys" onClose={onClose}
      actions={<button id="keybar-reset" onClick={() => onChange(null)}>Reset</button>}>
      <p className="dialog-help">The row under the terminal, left to right. Held arrows and ⌫ repeat.</p>
      <ol className="keybar-order">
        {row.map((item, index) => {
          const id = itemId(item),
            [label, spoken] = itemLabel(item);
          return (
            <li key={id} data-keybar-item={id}>
              <span className="keybar-chip" title={spoken}>{label}</span>
              <span className="keybar-spoken">{spoken}</span>
              <button aria-label={`Move ${spoken} left`} disabled={index === 0} onClick={() => onChange(move(row, index, -1))}>↑</button>
              <button aria-label={`Move ${spoken} right`} disabled={index === row.length - 1} onClick={() => onChange(move(row, index, 1))}>↓</button>
              <button className="keybar-remove" aria-label={`Remove ${spoken}`} onClick={() => onChange(row.filter((_, other) => other !== index))}>×</button>
            </li>
          );
        })}
        {!row.length && <li className="keybar-empty">No keys. Add some below, or Reset.</li>}
      </ol>
      <h3>Add a key</h3>
      <div className="keybar-palette">
        {missingBuiltins(row).map((item) => (
          <button key={itemId(item)} data-add-key={itemId(item)} aria-label={`Add ${itemLabel(item)[1]}`} onClick={() => onChange(addItem(row, item))}>
            {itemLabel(item)[0]}
          </button>
        ))}
      </div>
      <h3>Add a combination</h3>
      <div className="keybar-combo">
        <label><input type="checkbox" id="combo-ctrl" checked={combo.ctrl} onChange={(e) => setCombo({ ...combo, ctrl: e.target.checked })} />Ctrl</label>
        <label><input type="checkbox" id="combo-alt" checked={combo.alt} onChange={(e) => setCombo({ ...combo, alt: e.target.checked })} />Alt</label>
        <label><input type="checkbox" id="combo-shift" checked={combo.shift} onChange={(e) => setCombo({ ...combo, shift: e.target.checked })} />Shift</label>
        <select id="combo-key" aria-label="Key" value={comboKeys.includes(combo.key as BuiltinKey) ? combo.key : "custom"}
          onChange={(e) => setCombo({ ...combo, key: e.target.value })}>
          <option value="custom">Letter or symbol…</option>
          {comboKeys.map((key) => <option key={key} value={key}>{builtinLabels[key][1].replace(/^Send /, "")}</option>)}
        </select>
        {(combo.key === "custom" || !(combo.key in builtinKeys)) && (
          <input id="combo-char" aria-label="Letter or symbol" maxLength={1} autoCapitalize="off" autoCorrect="off" spellCheck={false}
            value={combo.key === "custom" ? custom : combo.key}
            onChange={(e) => { setCustom(e.target.value); setCombo({ ...combo, key: "custom" }); }} />
        )}
        <button id="combo-add" disabled={!comboOk || present.has(itemId(comboItem))} onClick={() => onChange(addItem(row, comboItem))}>
          Add {comboOk ? itemLabel(comboItem)[0] : ""}
        </button>
      </div>
      <h3>Add a saved reply</h3>
      <div className="keybar-palette">
        {snippets.map((snippet) => {
          const item = snippetItem(snippet);
          return (
            <button key={itemId(item)} data-add-reply={snippet.text} disabled={present.has(itemId(item))} onClick={() => onChange(addItem(row, item))}>
              {itemLabel(item)[0]}
            </button>
          );
        })}
        {!snippets.length && <p className="dialog-help">Save replies with ⚡ first; they can then sit on the row.</p>}
      </div>
      {row !== defaultKeybar && <p className="dialog-help">Saved on this device.</p>}
    </Dialog>
  );
}
