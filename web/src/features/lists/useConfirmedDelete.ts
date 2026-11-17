import { useState } from 'react';
import { useT } from '../../i18n/useT.ts';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';

interface ConfirmedDeleteBase<Row> {
  /** The question asked before deleting `row`. */
  message: (row: Row) => string;
  /** The toast when the request fails without a message of its own. */
  failed: string;
  /** Deletes one row by id (a mutation's `mutateAsync`). */
  remove: (id: string) => Promise<unknown>;
  /** The confirm button's label; "Delete" by default. */
  confirmLabel?: string;
  /** Runs once the request has succeeded (the success toast). */
  onDone?: (row: Row) => void;
}

/** A row that is not keyed by `id` says which field is (a member's user id). */
export type ConfirmedDeleteOptions<Row> = ConfirmedDeleteBase<Row> & (Row extends { id: string } ? { rowId?: (row: Row) => string } : { rowId: (row: Row) => string });

/**
 * useConfirmedDelete is deleting one row of a list as its row menu does it:
 * confirm, delete, a toast on failure. `pending` holds the ids whose request
 * is running, so only that row's entry is disabled.
 */
export function useConfirmedDelete<Row>(opts: ConfirmedDeleteOptions<Row>): { run: (row: Row) => Promise<void>; pending: ReadonlySet<string> } {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());
  async function run(row: Row): Promise<void> {
    if (!(await ask({ message: opts.message(row), confirmLabel: opts.confirmLabel ?? t.common.delete, destructive: true }))) return;
    const id = opts.rowId ? opts.rowId(row) : (row as { id: string }).id;
    setPending((p) => new Set(p).add(id));
    try {
      await opts.remove(id);
      opts.onDone?.(row);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : opts.failed, 'destructive');
    } finally {
      setPending((p) => {
        const next = new Set(p);
        next.delete(id);
        return next;
      });
    }
  }
  return { run, pending };
}
