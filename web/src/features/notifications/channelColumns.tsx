import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { NotifyChannel } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';

function target(c: NotifyChannel): string {
  if (c.email) return (c.email.recipients ?? []).join(', ');
  return c.webhook ? `${c.webhook.method} ${c.webhook.url}` : '';
}

/**
 * channelColumns is every column the notification channel list offers (list
 * id `channels`), sorted in the browser. A channel opens nothing (there is
 * no channel page: its fields and delivery log fit a dialog), so its name
 * is plain text and everything it can do sits in the row menu.
 */
export function channelColumns(t: Messages): DataListColumn<NotifyChannel>[] {
  const m = t.notifications;
  const c8 = m.channels;
  return [
    {
      id: 'channel',
      label: c8.columns.channel,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'name',
      sortValue: (c) => c.name,
      required: true,
      defaultVisible: true,
      render: (c) => <span className="block truncate font-medium">{c.name}</span>,
    },
    {
      id: 'kind',
      label: c8.columns.kind,
      width: '100px',
      sortKey: 'kind',
      sortValue: (c) => c.kind,
      defaultVisible: true,
      render: (c) => <span>{c.kind === 'email' ? c8.email : c8.webhook}</span>,
    },
    {
      id: 'sends_to',
      label: c8.columns.sendsTo,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'sends_to',
      sortValue: (c) => target(c),
      defaultVisible: true,
      render: (c) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{target(c)}</span>,
    },
    {
      id: 'status',
      label: c8.columns.status,
      width: '100px',
      sortKey: 'status',
      sortValue: (c) => (c.enabled ? 0 : 1),
      defaultVisible: true,
      render: (c) => <span>{c.enabled ? <Pill tone="ok">{m.enabled}</Pill> : <Pill tone="unknown">{m.disabled}</Pill>}</span>,
    },
  ];
}
