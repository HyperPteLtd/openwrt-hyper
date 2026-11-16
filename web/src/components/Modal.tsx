import type { ComponentChildren } from "preact";
import { useEffect, useId, useRef } from "preact/hooks";
import { t as tr } from "../lib/i18n";

/** Native modal supplies focus containment, with scroll locking and focus restoration. */
export function Modal({ open, title, desc, children, onClose, size = "normal" }: { open: boolean; title: string; desc?: string; children: ComponentChildren; onClose: () => void; size?: "normal" | "wide" }) {
  const dialog = useRef<HTMLDialogElement>(null);
  const titleId = useId();
  const descId = useId();
  useEffect(() => {
    if (!open || !dialog.current) return;
    const node = dialog.current;
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    node.showModal();
    return () => {
      node.close();
      document.body.style.overflow = overflow;
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, [open]);
  if (!open) return null;
  return (
    <dialog ref={dialog} class={`modal ${size === "wide" ? "modal-wide" : ""}`} aria-labelledby={titleId} aria-describedby={desc ? descId : undefined} onCancel={(e) => { e.preventDefault(); onClose(); }} onClose={onClose}>
      <div class="modal-header">
        <div><h2 id={titleId}>{title}</h2>{desc && <p id={descId} class="muted">{desc}</p>}</div>
        <button type="button" class="btn ghost icon modal-close" aria-label={tr("关闭")} onClick={onClose}>
          <svg aria-hidden="true" width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"><path d="m6 6 12 12M18 6 6 18" /></svg>
        </button>
      </div>
      <div class="modal-body">{children}</div>
    </dialog>
  );
}
