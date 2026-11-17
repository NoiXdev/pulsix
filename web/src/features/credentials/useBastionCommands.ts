import { useState } from 'react';
import { useConfirm } from '../../ui/confirmContext.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useClearBastionKey, useDeleteBastion, useSetBastionKey } from '../../api/hooks/useSsh.ts';
import type { Bastion } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

export interface BastionCommands {
  /** Asks first, then deletes; the server's refusal (still in use) is shown as the toast. */
  remove: (b: Bastion) => Promise<void>;
  /** Pins the fingerprint the newest connection test observed. */
  pin: (b: Bastion) => Promise<void>;
  unpin: (b: Bastion) => Promise<void>;
  /** The jump hosts whose pin or unpin is running: each disables only its own entry. */
  keyPending: ReadonlySet<string>;
}

/**
 * useBastionCommands is the jump host list's confirm/toast mutations: the
 * row menu's Delete, Pin host key and Unpin. The pin state is tracked per
 * row, never as one flag over the list.
 */
export function useBastionCommands(org: string): BastionCommands {
  const t = useT();
  const toast = useToast();
  const ask = useConfirm();
  const del = useDeleteBastion(org);
  const setKey = useSetBastionKey(org);
  const clearKey = useClearBastionKey(org);
  const [keyPending, setKeyPending] = useState<ReadonlySet<string>>(new Set());
  const m = t.credentials.jumpHosts;

  async function remove(b: Bastion) {
    if (!(await ask({ message: m.confirmDelete(b.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await del.mutateAsync(b.id);
      toast.show(m.deleted(b.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  async function withKeyPending(b: Bastion, run: () => Promise<void>) {
    if (keyPending.has(b.id)) return;
    setKeyPending((p) => new Set(p).add(b.id));
    try {
      await run();
    } finally {
      setKeyPending((p) => {
        const next = new Set(p);
        next.delete(b.id);
        return next;
      });
    }
  }

  const pin = (b: Bastion) =>
    withKeyPending(b, async () => {
      if (!b.observed_key) return;
      try {
        await setKey.mutateAsync({ id: b.id, fingerprint: b.observed_key });
        toast.show(m.pinned(b.name));
      } catch (err) {
        toast.show(err instanceof Error ? err.message : t.hosts.connection.pinFailed, 'destructive');
      }
    });

  const unpin = (b: Bastion) =>
    withKeyPending(b, async () => {
      try {
        await clearKey.mutateAsync(b.id);
        toast.show(m.pinCleared);
      } catch (err) {
        toast.show(err instanceof Error ? err.message : t.hosts.connection.unpinFailed, 'destructive');
      }
    });

  return { remove, pin, unpin, keyPending };
}
