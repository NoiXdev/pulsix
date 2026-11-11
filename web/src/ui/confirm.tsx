import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useT } from '../i18n/useT.ts';
import { Button } from './button.tsx';
import { Dialog } from './dialog.tsx';
import { ConfirmContext, type ConfirmFn, type ConfirmOptions } from './confirmContext.ts';

interface Pending {
  options: ConfirmOptions;
  resolve: (answer: boolean) => void;
  /** What had focus when the question was asked, to give it back. */
  opener: Element | null;
  settled: boolean;
}

/**
 * ConfirmProvider renders the one confirmation dialog the whole app shares
 * and hands out `confirm(options): Promise<boolean>` (see useConfirm in
 * ./confirmContext.ts). Mount it once near the root, inside the I18nProvider.
 *
 * - Focus starts on Cancel, so Enter never confirms a deletion by accident,
 *   and returns to the opener afterwards.
 * - Escape and any other close resolve `false`.
 * - Only one question is on screen: a second `confirm()` while one is open
 *   waits in a queue and is shown when the first is answered. Every promise
 *   is resolved — those still pending when the provider unmounts with `false`.
 * - The dialog sits at the root, so it is opened after any dialog the
 *   question came from and stacks above it in the top layer.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const t = useT();
  const [current, setCurrent] = useState<Pending | null>(null);
  const currentRef = useRef<Pending | null>(null);
  const queue = useRef<Pending[]>([]);
  const cancelRef = useRef<HTMLButtonElement>(null);

  const show = useCallback((next: Pending | null) => {
    currentRef.current = next;
    setCurrent(next);
  }, []);

  const confirm = useCallback<ConfirmFn>(
    (options) =>
      new Promise<boolean>((resolve) => {
        const pending: Pending = { options, resolve, opener: document.activeElement, settled: false };
        if (currentRef.current) queue.current.push(pending);
        else show(pending);
      }),
    [show],
  );

  const settle = useCallback(
    (pending: Pending, answer: boolean) => {
      if (pending.settled) return;
      pending.settled = true;
      pending.resolve(answer);
      const next = queue.current.shift() ?? null;
      show(next);
      if (!next && pending.opener instanceof HTMLElement && pending.opener.isConnected) pending.opener.focus();
    },
    [show],
  );

  // Runs after the Dialog's own effect (a child's effects come first), so
  // the dialog is already modal and the focus sticks.
  useEffect(() => {
    if (current) cancelRef.current?.focus();
  }, [current]);

  useEffect(() => {
    const waiting = queue.current;
    return () => {
      const active = currentRef.current;
      if (active && !active.settled) {
        active.settled = true;
        active.resolve(false);
      }
      for (const p of waiting.splice(0)) {
        p.settled = true;
        p.resolve(false);
      }
    };
  }, []);

  const value = useMemo(() => confirm, [confirm]);
  const options = current?.options;

  return (
    <ConfirmContext.Provider value={value}>
      {children}
      <Dialog
        open={current !== null}
        onClose={() => {
          if (currentRef.current) settle(currentRef.current, false);
        }}
        title={options?.title ?? t.ui.confirm.title}
      >
        <p className="text-[13px] text-foreground">{options?.message}</p>
        <div className="mt-5 flex justify-end gap-2">
          <Button ref={cancelRef} type="button" onClick={() => current && settle(current, false)}>
            {t.ui.confirm.cancel}
          </Button>
          <Button
            type="button"
            variant={options?.destructive ? 'destructive' : 'primary'}
            onClick={() => current && settle(current, true)}
          >
            {options?.confirmLabel ?? t.ui.confirm.confirm}
          </Button>
        </div>
      </Dialog>
    </ConfirmContext.Provider>
  );
}
