import { Pill } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem, StaticListRow } from '../../ui/list-row.tsx';
import { useT } from '../../i18n/useT.ts';
import type { NotifyChannel } from '../../api/types.ts';

function target(c: NotifyChannel): string {
  if (c.email) return (c.email.recipients ?? []).join(', ');
  return c.webhook ? `${c.webhook.method} ${c.webhook.url}` : '';
}

/** ChannelRow is one notification channel. It opens nothing (there is no
 * channel page: its four fields and delivery log fit the row and a dialog), so
 * its ⋯ menu carries the actions. Everyone who can read channels may open the
 * delivery log; Send test, Edit and Delete… need `channels.write`. `columns`
 * reserves the menu's 40px as its last track, left empty here. While this
 * channel's test is in flight, its Send test reads "Sending test" and is
 * disabled: a second message to the receiver would be a duplicate. */
export function ChannelRow({
  channel: c, columns, canWrite, testing, onLog, onTest, onEdit, onDelete,
}: {
  channel: NotifyChannel;
  columns: string;
  canWrite: boolean;
  testing: boolean;
  onLog: (c: NotifyChannel) => void;
  onTest: (c: NotifyChannel) => void;
  onEdit: (c: NotifyChannel) => void;
  onDelete: (c: NotifyChannel) => void;
}) {
  const t = useT();
  const m = t.notifications;
  const c8 = m.channels;
  return (
    <StaticListRow
      columns={columns}
      menu={
        <RowMenu label={t.common.rowActions(c.name)}>
          <RowMenuItem onSelect={() => onLog(c)}>{c8.deliveriesItem}</RowMenuItem>
          {canWrite && (
            <>
              <RowMenuItem onSelect={() => onTest(c)} disabled={testing}>
                {testing ? c8.testingItem : c8.testItem}
              </RowMenuItem>
              <RowMenuItem onSelect={() => onEdit(c)}>{m.edit}</RowMenuItem>
              <RowMenuItem onSelect={() => onDelete(c)} destructive>
                {c8.deleteItem}
              </RowMenuItem>
            </>
          )}
        </RowMenu>
      }
    >
      <span className="truncate text-[13px] font-medium" data-row>
        {c.name}
      </span>
      <span className="text-xs">{c.kind === 'email' ? c8.email : c8.webhook}</span>
      <span className="truncate font-mono text-xs text-muted-foreground">{target(c)}</span>
      <span>{c.enabled ? <Pill tone="ok">{m.enabled}</Pill> : <Pill tone="unknown">{m.disabled}</Pill>}</span>
      <span />
    </StaticListRow>
  );
}
