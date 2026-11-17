import { useState, type ReactNode } from 'react';
import { useMutationState } from '@tanstack/react-query';
import { testChannelKey, useDeleteChannel, useTestChannel } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { useToast } from '../../ui/toastContext.ts';
import { useConfirmedDelete } from '../lists/useConfirmedDelete.ts';
import { ChannelDialog } from './ChannelDialog.tsx';
import { DeliveryLog } from './DeliveryLog.tsx';

/**
 * useChannelListActions is the channel list's row menu: Deliveries for
 * everyone who can read channels; Send test, Edit and Delete… need
 * `channels.write`. While a channel's test is in flight, its Send test reads
 * "Sending test" and is disabled — a second message to the receiver would be
 * a duplicate — and only that channel's entry is. `dialogs` holds the
 * channel dialog (an entry or `openNew`) and the delivery log.
 */
export function useChannelListActions(
  org: string,
  opts: { canWrite: boolean; mailAvailable: boolean },
): { rowActions: (channel: NotifyChannel) => RowAction[]; dialogs: ReactNode; openNew: () => void } {
  const t = useT();
  const m = t.notifications;
  const c8 = m.channels;
  const toast = useToast();
  const test = useTestChannel(org);
  // The channels with a test in flight, each on its own: one hook serves
  // every row, so its own isPending would hold back Send test on all of
  // them while one is sending.
  const testing = useMutationState({
    filters: { mutationKey: testChannelKey(org), status: 'pending' },
    select: (mutation) => mutation.state.variables as string,
  });
  const remove = useDeleteChannel(org);
  const [editing, setEditing] = useState<NotifyChannel | 'new' | null>(null);
  const [logOf, setLogOf] = useState<NotifyChannel | null>(null);
  const del = useConfirmedDelete<NotifyChannel>({
    message: (c) => c8.deleteConfirm(c.name),
    failed: m.deleteFailed,
    remove: async (id) => {
      await remove.mutateAsync(id);
    },
  });

  async function onTest(c: NotifyChannel) {
    try {
      const res = await test.mutateAsync(c.id);
      if (res.delivery.status === 'sent') toast.show(c8.testSent(c.name));
      else toast.show(c8.testFailed(c.name, res.delivery.last_error ?? c8.unknownError), 'destructive');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : c8.testRequestFailed, 'destructive');
    }
  }

  function rowActions(c: NotifyChannel): RowAction[] {
    const items: RowAction[] = [{ id: 'deliveries', label: c8.deliveriesItem, onSelect: () => setLogOf(c) }];
    if (!opts.canWrite) return items;
    const sending = testing.includes(c.id);
    items.push(
      { id: 'test', label: sending ? c8.testingItem : c8.testItem, onSelect: () => void onTest(c), disabled: sending },
      { id: 'edit', label: m.edit, onSelect: () => setEditing(c) },
      { id: 'delete', label: c8.deleteItem, destructive: true, onSelect: () => void del.run(c), disabled: del.pending.has(c.id) },
    );
    return items;
  }

  return {
    rowActions,
    openNew: () => setEditing('new'),
    dialogs: (
      <>
        {editing && (
          <ChannelDialog
            org={org}
            channel={editing === 'new' ? undefined : editing}
            mailAvailable={opts.mailAvailable}
            onClose={() => setEditing(null)}
            // This page's own list already refreshes via useCreateChannel's
            // invalidation, and nothing here needs the new id for itself (it
            // isn't selecting a channel into another field) — unlike
            // RotationsPage's stand-in picker, which is why onCreated exists.
            onCreated={() => undefined}
          />
        )}
        {logOf && <DeliveryLog org={org} channel={logOf} canWrite={opts.canWrite} onClose={() => setLogOf(null)} />}
      </>
    ),
  };
}
