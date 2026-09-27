// The phone's key row (keybar.ts) and the dialog that arranges it. Holding
// an arrow or Backspace repeats it, like a hardware key; a drag along the
// row scrolls it without pressing anything.
import { useEffect, useRef, useState } from "react";
import { Dialog } from "./dialogs";
import "./keybar.css";
import type { Mods } from "./keys";
import type { QuickCommand } from "../quick/commands";
import { haptic } from "../mobile/haptics";
import { t, useLocale } from "../i18n";
import {
  addItem,
  builtinKeys,
  builtinLabel,
  itemBytes,
  itemId,
  itemLabel,
  missingBuiltins,
  move,
  quickItem,
  repeats,
  validComboKey,
  type BuiltinKey,
  type KeyItem,
} from "./keybar";

const REPEAT_DELAY = 380,
  REPEAT_EVERY = 55;

export function Keybar({
  row,
  quick,
  disabled,
  mods,
  appCursor,
  encode,
  onMod,
  onSend,
  onSnippets,
  onFind,
  onEdit,
}: {
  row: KeyItem[];
  /** The quick commands this terminal offers (its project's, then everyone's). */
  quick: QuickCommand[];
  disabled: boolean;
  mods: Mods;
  appCursor: () => boolean;
  /** The extended encoding of a key, when a program asked for one (engine.modify). */
  encode?: (text: string, mods: Mods) => string | undefined;
  onMod: (id: "ctrl" | "alt") => void;
  onSend: (bytes: string) => void;
  onSnippets: () => void;
  onFind: () => void;
  onEdit: () => void;
}) {
  useLocale();
  const hold = useRef<{ id: number; x: number; timer?: number; interval?: number; fired: boolean }>(undefined);
  const stop = () => {
    const h = hold.current;
    if (!h) return;
    clearTimeout(h.timer);
    clearInterval(h.interval);
  };
  useEffect(() => stop, []);
  const armed = mods.ctrl || mods.alt;
  const byId = new Map(quick.map((command) => [command.id, command]));
  return (
    <div id="terminal-keybar" role="group" aria-label={t("keybar.group")}>
      {row.map((item) => {
        const command = item.t === "quick" ? byId.get(item.id) : undefined;
        // A key for a quick command that was deleted, or that belongs to
        // another project's terminals, is not shown here.
        if (item.t === "quick" && !command) return null;
        const id = itemId(item),
          [label, spoken] = itemLabel(item, command);
        const send = () => onSend(itemBytes(item, !armed && appCursor(), command, armed ? undefined : encode));
        // Find works with no connection; everything else types into it.
        const off = item.t === "find" ? false : disabled;
        return (
          <button
            key={id}
            data-terminal-key={id}
            className={item.t === "mod" ? "modifier" : item.t === "snippets" ? "snippets" : item.t === "quick" ? "quick" : undefined}
            aria-label={spoken}
            title={item.t === "snippets" || item.t === "find" ? spoken : undefined}
            aria-pressed={item.t === "mod" ? mods[item.id] : undefined}
            disabled={off}
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
              else if (item.t === "find") onFind();
              else send();
            }}
          >
            {label}
          </button>
        );
      })}
      <button data-terminal-key="edit" className="keybar-edit" aria-label={t("keybar.customize")} title={t("keybar.customize")}
        onPointerDown={(event) => event.preventDefault()} onClick={onEdit}>
        ✎
      </button>
    </div>
  );
}

const comboKeys: BuiltinKey[] = ["left", "right", "up", "down", "home", "end", "pageup", "pagedown", "tab", "enter", "backspace", "delete", "escape"];

export function KeybarEditor({
  row,
  quick,
  onChange,
  onClose,
}: {
  row: KeyItem[];
  quick: QuickCommand[];
  onChange: (row: KeyItem[] | null) => void;
  onClose: () => void;
}) {
  useLocale();
  const [combo, setCombo] = useState({ key: "r", ctrl: true, alt: false, shift: false });
  const [custom, setCustom] = useState("");
  const comboKey = combo.key === "custom" ? custom : combo.key;
  const comboItem: KeyItem = { t: "combo", key: comboKey, ctrl: combo.ctrl, alt: combo.alt, shift: combo.shift };
  const comboOk = validComboKey(comboKey) && (combo.ctrl || combo.alt || combo.shift);
  const present = new Set(row.map(itemId));
  const byId = new Map(quick.map((command) => [command.id, command]));
  return (
    <Dialog id="keybar-dialog" title={t("keybar.customize")} onClose={onClose}
      actions={<button id="keybar-reset" onClick={() => onChange(null)}>{t("keybar.reset")}</button>}>
      <p className="dialog-help">{t("keybar.help")}</p>
      <ol className="keybar-order">
        {row.map((item, index) => {
          const id = itemId(item),
            [label, spoken] = itemLabel(item, item.t === "quick" ? byId.get(item.id) : undefined);
          return (
            <li key={id} data-keybar-item={id}>
              <span className="keybar-chip" title={spoken}>{label}</span>
              <span className="keybar-spoken">{spoken}</span>
              <button aria-label={t("keybar.moveLeft", { key: spoken })} disabled={index === 0} onClick={() => onChange(move(row, index, -1))}>↑</button>
              <button aria-label={t("keybar.moveRight", { key: spoken })} disabled={index === row.length - 1} onClick={() => onChange(move(row, index, 1))}>↓</button>
              <button className="keybar-remove" aria-label={t("keybar.remove", { key: spoken })} onClick={() => onChange(row.filter((_, other) => other !== index))}>×</button>
            </li>
          );
        })}
        {!row.length && <li className="keybar-empty">{t("keybar.empty")}</li>}
      </ol>
      <h3>{t("keybar.addKey")}</h3>
      <div className="keybar-palette">
        {missingBuiltins(row).map((item) => (
          <button key={itemId(item)} data-add-key={itemId(item)} aria-label={t("keybar.add", { key: itemLabel(item)[1] })} onClick={() => onChange(addItem(row, item))}>
            {itemLabel(item)[0]}
          </button>
        ))}
      </div>
      <h3>{t("keybar.addCombo")}</h3>
      <div className="keybar-combo">
        <label><input type="checkbox" id="combo-ctrl" checked={combo.ctrl} onChange={(e) => setCombo({ ...combo, ctrl: e.target.checked })} />Ctrl</label>
        <label><input type="checkbox" id="combo-alt" checked={combo.alt} onChange={(e) => setCombo({ ...combo, alt: e.target.checked })} />Alt</label>
        <label><input type="checkbox" id="combo-shift" checked={combo.shift} onChange={(e) => setCombo({ ...combo, shift: e.target.checked })} />Shift</label>
        <select id="combo-key" aria-label={t("keybar.comboKey")} value={comboKeys.includes(combo.key as BuiltinKey) ? combo.key : "custom"}
          onChange={(e) => setCombo({ ...combo, key: e.target.value })}>
          <option value="custom">{t("keybar.letterOrSymbol")}</option>
          {comboKeys.map((key) => <option key={key} value={key}>{builtinLabel(key)[1]}</option>)}
        </select>
        {(combo.key === "custom" || !(combo.key in builtinKeys)) && (
          <input id="combo-char" aria-label={t("keybar.letterOrSymbol")} maxLength={1} autoCapitalize="off" autoCorrect="off" spellCheck={false}
            value={combo.key === "custom" ? custom : combo.key}
            onChange={(e) => { setCustom(e.target.value); setCombo({ ...combo, key: "custom" }); }} />
        )}
        <button id="combo-add" disabled={!comboOk || present.has(itemId(comboItem))} onClick={() => onChange(addItem(row, comboItem))}>
          {t("keybar.add", { key: comboOk ? itemLabel(comboItem)[0] : "" })}
        </button>
      </div>
      <h3>{t("keybar.addQuick")}</h3>
      <div className="keybar-palette">
        {quick.map((command) => {
          const item = quickItem(command);
          return (
            <button key={itemId(item)} data-add-quick={command.text} disabled={present.has(itemId(item))} onClick={() => onChange(addItem(row, item))}>
              {itemLabel(item, command)[0]}
            </button>
          );
        })}
        {!quick.length && <p className="dialog-help">{t("keybar.noQuick")}</p>}
      </div>
    </Dialog>
  );
}
