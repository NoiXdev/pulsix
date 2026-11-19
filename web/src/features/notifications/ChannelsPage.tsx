import { useParams } from '@tanstack/react-router';
import { Bell } from 'lucide-react';
import { DataList } from '../../ui/data-list/index.ts';
import { Button } from '../../ui/button.tsx';
import { ListToolbar, ResetFilters, ResultCount } from '../../ui/list-toolbar.tsx';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChannels } from '../../api/hooks/useNotifications.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { channelColumns } from './channelColumns.tsx';
import { useChannelListActions } from './useChannelListActions.tsx';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { CHANNEL_KINDS, CHANNELS_FILTERS, ENABLED_STATES, filterChannels } from './notificationsSearch.ts';
import { DigestCard } from './DigestCard.tsx';
import { PageHeader } from '../../ui/settings.tsx';

/** ChannelsPage is Settings → Notifications (spec 2026-09-18 §5): the
 * organization's e-mail and webhook channels with Send test and each
 * channel's delivery log. Operators see them; admins manage them. */
export function ChannelsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const m = t.notifications;
  const c8 = m.channels;
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const channels = useChannels(org, !!orgInfo.data && canRead);
  const actions = useChannelListActions(org, { canWrite, mailAvailable: channels.data?.mail_available ?? false });
  const columns = channelColumns(t);
  const { filters, active, setFilter, reset } = useListFilters(CHANNELS_FILTERS);
  const view = useListView('channels', columns, { mode: 'client', rowKey: (c) => c.id, resetKey: JSON.stringify(filters) });

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
  if (!canRead) {
    return (
      <>
        <PageHeader title={m.title} />
        <p className="text-sm text-muted-foreground">{c8.noAccess}</p>
      </>
    );
  }
  if (!channels.data) return <QueryLoading />;
  const list = channels.data.channels ?? [];
  const mailAvailable = channels.data.mail_available;
  const shown = filterChannels(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);

  return (
    <>
      <PageHeader
        title={m.title}
        description={c8.subtitle}
        actions={
          canWrite ? (
            <Button variant="primary" onClick={actions.openNew}>
              <Bell size={15} /> {c8.add}
            </Button>
          ) : undefined
        }
      />
      {!mailAvailable && (
        <DisabledHint title={c8.mailOffTitle} body={c8.mailNote} />
      )}
      <DataList
        label={c8.label}
        columns={columns}
        rows={rows}
        rowKey={(c) => c.id}
        rowLabel={(c) => c.name}
        rowActions={actions.rowActions}
        onRowActivate={actions.edit}
        loading={!view.ready}
        empty={active ? c8.noMatch : c8.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={c8.count} />}>
            <FilterSelect
              label={c8.filters.kind}
              allLabel={c8.filters.allKinds}
              options={CHANNEL_KINDS.map((k) => ({ value: k, label: k === 'email' ? c8.email : c8.webhook }))}
              value={filters.kind}
              onChange={(v) => setFilter('kind', v as typeof filters.kind)}
            />
            <FilterSelect
              label={c8.filters.state}
              allLabel={c8.filters.anyState}
              options={ENABLED_STATES.map((v) => ({ value: v, label: v === 'enabled' ? m.enabled : m.disabled }))}
              value={filters.state}
              onChange={(v) => setFilter('state', v as typeof filters.state)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, c8.count)}
      />
      {/* A settings card: the settings column's width, not the table's. */}
      <div className="max-w-[880px]">
        <DigestCard org={org} channels={list} canWrite={canWrite} />
      </div>
      {actions.dialogs}
    </>
  );
}
