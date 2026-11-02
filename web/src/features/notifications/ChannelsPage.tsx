import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Bell } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { SettingsTabs } from '../settings/SettingsTabs.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChannels, useDeleteChannel, useTestChannel } from '../../api/hooks/useNotifications.ts';
import { may } from '../../lib/roles.ts';
import type { NotifyChannel } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { ChannelDialog } from './ChannelDialog.tsx';
import { DeliveryLog } from './DeliveryLog.tsx';
import { DigestCard } from './DigestCard.tsx';

const COLUMNS = 'minmax(0, 0.8fr) 90px minmax(0, 1.2fr) 90px 260px';

function target(c: NotifyChannel): string {
  if (c.email) return (c.email.recipients ?? []).join(', ');
  return c.webhook ? `${c.webhook.method} ${c.webhook.url}` : '';
}

/** ChannelsPage is Settings → Notifications (spec 2026-09-18 §5): the
 * organization's e-mail and webhook channels with Send test and each
 * channel's delivery log. Operators see them; admins manage them. */
export function ChannelsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const m = t.notifications;
  const c8 = m.channels;
  const toast = useToast();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const channels = useChannels(org, !!orgInfo.data && canRead);
  const test = useTestChannel(org);
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
    <div className="flex flex-col gap-2">
      <h1 className="text-xl font-semibold">{m.title}</h1>
      <SettingsTabs org={org} />
    </div>
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
    if (!confirm(c8.deleteConfirm(c.name))) return;
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
        <p className="text-xs text-muted-foreground" role="note">
          {c8.mailNote}
        </p>
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
          <GridRow key={c.id} columns={COLUMNS}>
            <span className="truncate text-[13px] font-medium" data-row>
              {c.name}
            </span>
            <span className="text-xs">{c.kind === 'email' ? c8.email : c8.webhook}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{target(c)}</span>
            <span>{c.enabled ? <Pill tone="ok">{m.enabled}</Pill> : <Pill tone="unknown">{m.disabled}</Pill>}</span>
            <span className="flex justify-end gap-1">
              <Button type="button" variant="ghost" aria-label={c8.logAria(c.name)} onClick={() => setLogOf(c)}>
                {c8.log}
              </Button>
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={c8.testAria(c.name)} onClick={() => void onTest(c)} disabled={test.isPending}>
                    {c8.test}
                  </Button>
                  <Button type="button" variant="ghost" aria-label={m.editAria(c.name)} onClick={() => setEditing(c)}>
                    {m.edit}
                  </Button>
                  <Button type="button" variant="destructive" aria-label={m.deleteAria(c.name)} onClick={() => void onDelete(c)}>
                    {t.common.delete}
                  </Button>
                </>
              )}
            </span>
          </GridRow>
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
