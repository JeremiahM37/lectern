import { useContext, useEffect, useRef, type ComponentProps } from "react";
import { PaneEmbedContext } from "../workspace/registry";
export function Modal({
  open: _open,
  onClick,
  onCancel,
  onKeyDown,
  ...props
}: ComponentProps<"dialog">) {
  const ref = useRef<HTMLDialogElement>(null);
  // In a workspace pane the dialog sits in the layout instead of over it.
  const embedded = useContext(PaneEmbedContext);
  useEffect(() => {
    const prior = document.activeElement as HTMLElement | null;
    const d = ref.current;
    if (d && !d.open) {
      if (embedded) d.show();
      else d.showModal();
    }
    return () => {
      if (d?.open) d.close();
      if (!embedded) prior?.focus?.();
    };
  }, []);
  return (
    <dialog
      {...props}
      ref={ref}
      onCancel={onCancel}
      onKeyDown={(event) => {
        onKeyDown?.(event);
        if (event.defaultPrevented || event.key !== "Tab") return;
        const items = Array.from(
          event.currentTarget.querySelectorAll<HTMLElement>(
            "button,input,select,textarea,a[href],summary,[tabindex]",
          ),
        ).filter(
          (el) =>
            !el.matches(':disabled,[tabindex="-1"]') &&
            el.getClientRects().length > 0 &&
            !el.closest("[hidden]"),
        );
        const first = items[0],
          last = items.at(-1);
        if (event.shiftKey && document.activeElement === first && last) {
          event.preventDefault();
          last.focus();
        } else if (
          !event.shiftKey &&
          document.activeElement === last &&
          first
        ) {
          event.preventDefault();
          first.focus();
        }
      }}
      onClick={(event) => {
        onClick?.(event);
        if (event.defaultPrevented || event.target !== event.currentTarget)
          return;
        const box = event.currentTarget.getBoundingClientRect();
        const inside =
          event.clientX >= box.left &&
          event.clientX <= box.right &&
          event.clientY >= box.top &&
          event.clientY <= box.bottom;
        if (!inside) {
          event.currentTarget.dispatchEvent(
            new Event("cancel", { cancelable: true }),
          );
        }
      }}
    />
  );
}
