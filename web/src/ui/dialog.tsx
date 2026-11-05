import { useEffect, useId, useRef, type ReactNode } from 'react';

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  /** 'wide' is for a form with a lot to show at once (e.g. a generated
   * assignment form next to the group's read-only values); default fits
   * every other dialog in the app. */
  size?: 'default' | 'wide';
  /** false asks the browser not to close the dialog on Escape (it prevents
   * the native `cancel`), for while a request it started is in flight. It is
   * a request, not a guarantee: under the close-watcher rules a `cancel` is
   * cancelable only with user activation, so a repeated Escape can still
   * close the dialog. A caller must not keep anything it cannot lose in the
   * dialog's own lifetime (see features/users/useAccountActionRunner.ts). */
  dismissible?: boolean;
}

const SIZE_CLASS: Record<NonNullable<DialogProps['size']>, string> = {
  default: 'w-[480px]',
  wide: 'w-[680px]',
};

/**
 * Dialog wraps the native <dialog> element (showModal/close), which gives
 * focus trapping, ESC-to-close and a backdrop for free — no Radix
 * dependency needed for a single-purpose modal like the invite/create-
 * token/add-passkey forms this app uses it for.
 *
 * The title labels the dialog itself (`aria-labelledby`), not just a
 * heading inside it, so a test or assistive technology can find the
 * dialog by name: `getByRole('dialog', { name: 'Add credential' })`.
 */
export function Dialog({ open, onClose, title, children, size = 'default', dismissible = true }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      // Only `close`, not `cancel` (backlog 1.46). A native <dialog>
      // answers one Escape with BOTH events — `cancel` first, then
      // `close` once it is not prevented — so wiring the same handler to
      // each called it twice per key press. Every call site's closer
      // happened to be idempotent, which is why nothing broke; the first
      // one with a real side effect would have double-fired silently.
      // `close` is the one to keep: it also fires for the programmatic
      // el.close() below, which `cancel` does not.
      //
      // Both handlers act only on this dialog's own events: React passes a
      // `close` or `cancel` up the component tree like a bubbling event, so
      // a dialog opened from inside this one would otherwise close this one
      // too when it closes (or be kept open by this one's `dismissible`).
      onClose={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      // Escape sends `cancel` first; preventing it is the one way to keep
      // the dialog open (the `close` above then never fires).
      onCancel={(e) => {
        if (e.target === e.currentTarget && !dismissible) e.preventDefault();
      }}
      aria-labelledby={titleId}
      // m-auto is not decoration: a native <dialog> centres itself in the
      // viewport through the user-agent stylesheet's own `margin: auto`,
      // and Tailwind's preflight resets `margin: 0` on every element —
      // which drops every dialog in the app into the top-left corner. The
      // centring assertion in web/e2e/members.spec.ts is what pins it,
      // because jsdom does no layout and so no unit test can see it.
      className={`${SIZE_CLASS[size]} m-auto max-w-[90vw] rounded-xl border border-border bg-card p-0 text-foreground backdrop:bg-black/60`}
    >
      <div className="flex items-center justify-between border-b border-border px-5 py-4">
        <h2 id={titleId} className="text-[15px] font-semibold">
          {title}
        </h2>
      </div>
      <div className="p-5">{children}</div>
    </dialog>
  );
}
