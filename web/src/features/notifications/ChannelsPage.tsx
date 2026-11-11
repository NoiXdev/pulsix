import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { useMutationState } from '@tanstack/react-query';
import { Bell } from 'lucide-react';
import { Card, GridHeader } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { testChannelKey, useChannels, useDeleteChannel, useTestChannel } from '../../api/hooks/useNotifications.ts';
import { may } from '../../lib/roles.ts';
import type { NotifyChannel } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { ChannelDialog } from './ChannelDialog.tsx';
import { ChannelRow } from './ChannelRow.tsx';
import { DeliveryLog } from './DeliveryLog.tsx';
import { DigestCard } from './DigestCard.tsx';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS = 'minmax(0, 0.8fr) 90px minmax(0, 1.2fr) 90px 40px';

/** ChannelsPage is Settings → Notifications (spec 2026-09-18 §5): the
 * organization's e-mail and webhook channels with Send test and each
 * channel's delivery log. Operators see them; admins manage them. */
export function ChannelsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const m = t.notifications;
  const c8 = m.channels;
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const channels = useChannels(org, !!orgInfo.data && canRead);
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

  // Backlog 2.85: orgInfo was guarded, but the channel list itself was
  // not — a 5xx (or any other refusal) left `channels.data` undefined
  // forever and the page on its permanent "Loading…" skeleton, the same
  // silent-failure class the calendar's empty grid was. The guard renders
  // the error state instead; a refetch that succeeds brings the list back.
  const guard = useQueryGuard(orgInfo);
  const channelsGuard = useQueryGuard(channels);
  if (guard) return guard;
  if (channelsGuard) return channelsGuard;
  if (!orgInfo.data) return <QueryLoading />;
  const header = (
    <h1 className="text-xl font-semibold">{m.title}</h1>
  );
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{c8.noAccess}</p>
      </>
    );
  }
  if (!channels.data) return <QueryLoading />;
  const list = channels.data.channels ?? [];
  const mailAvailable = channels.data.mail_available;

  async function onTest(c: NotifyChannel) {
    try {
      const res = await test.mutateAsync(c.id);
      if (res.delivery.status === 'sent') toast.show(c8.testSent(c.name));
      else toast.show(c8.testFailed(c.name, res.delivery.last_error ?? c8.unknownError), 'destructive');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : c8.testRequestFailed, 'destructive');
    }
  }

  async function onDelete(c: NotifyChannel) {
    if (!(await ask({ message: c8.deleteConfirm(c.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(c.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">{c8.subtitle}</span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <Bell size={15} /> {c8.add}
          </Button>
        )}
      </div>
      {!mailAvailable && (
        <DisabledHint title={c8.mailOffTitle} body={c8.mailNote} />
      )}
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{c8.columns.channel}</span>
          <span>{c8.columns.kind}</span>
          <span>{c8.columns.sendsTo}</span>
          <span>{c8.columns.status}</span>
          <span />
        </GridHeader>
        {list.map((c) => (
          <ChannelRow
            key={c.id}
            channel={c}
            columns={COLUMNS}
            canWrite={canWrite}
            testing={testing.includes(c.id)}
            onLog={setLogOf}
            onTest={(ch) => void onTest(ch)}
            onEdit={setEditing}
            onDelete={(ch) => void onDelete(ch)}
          />
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{c8.empty}</p>}
      </Card>
      <DigestCard org={org} channels={list} canWrite={canWrite} />
      {editing && (
        <ChannelDialog
          org={org}
          channel={editing === 'new' ? undefined : editing}
          mailAvailable={mailAvailable}
          onClose={() => setEditing(null)}
          // This page's own list already refreshes via useCreateChannel's
          // invalidation, and nothing here needs the new id for itself (it
          // isn't selecting a channel into another field) — unlike
          // RotationsPage's stand-in picker, which is why onCreated exists.
          onCreated={() => undefined}
        />
      )}
      {logOf && <DeliveryLog org={org} channel={logOf} canWrite={canWrite} onClose={() => setLogOf(null)} />}
    </>
  );
}
