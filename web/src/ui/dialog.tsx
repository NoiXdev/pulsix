import { useEffect, useRef, type ReactNode } from 'react';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
}

/**
 * Dialog wraps the native <dialog> element (showModal/close), which gives
 * focus trapping, ESC-to-close and a backdrop for free — no Radix
 * dependency needed for a single-purpose modal like the invite/create-
 * token/add-passkey forms this app uses it for.
 */
export function Dialog({ open, onClose, title, children }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      onClose={onClose}
      onCancel={onClose}
      className="w-[480px] max-w-[90vw] rounded-xl border border-border bg-card p-0 text-foreground backdrop:bg-black/60"
    >
      <div className="flex items-center justify-between border-b border-border px-5 py-4">
        <h2 className="text-[15px] font-semibold">{title}</h2>
      </div>
      <div className="p-5">{children}</div>
    </dialog>
  );
}
