import { Dialog } from '../../ui/dialog.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useChannelDeliveries } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel } from '../../api/types.ts';

const STATUS_TONE: Record<string, Tone> = { sent: 'ok', pending: 'warn', failed: 'crit' };

/** DeliveryLog shows a channel's newest deliveries: what was sent, how
 * often it was tried, and the last error of a retried or failed one. */
export function DeliveryLog({ org, channel, onClose }: { org: string; channel: NotifyChannel; onClose: () => void }) {
  const log = useChannelDeliveries(org, channel.id);
  const rows = log.data?.deliveries ?? [];
  return (
    <Dialog open onClose={onClose} title={`Deliveries · ${channel.name}`} size="wide">
      {!log.data ? (
        <QueryLoading />
      ) : rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">Nothing sent through this channel yet.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-border">
          {rows.map((d) => (
            <li key={d.id} className="grid grid-cols-[140px_130px_70px_minmax(0,1fr)] items-center gap-3 py-2 text-xs" data-delivery>
              <span className="text-muted-foreground">{new Date(d.created_at).toLocaleString()}</span>
              <span className="font-mono">{d.event_kind}</span>
              <span>
                <Pill tone={STATUS_TONE[d.status] ?? 'unknown'}>{d.status}</Pill>
              </span>
              <span className="truncate text-muted-foreground" title={d.last_error}>
                {d.attempts > 1 ? `${d.attempts} attempts` : ''}
                {d.last_error ? ` · ${d.last_error}` : ''}
                {d.status === 'pending' && d.next_attempt_at ? ` · next try ${new Date(d.next_attempt_at).toLocaleTimeString()}` : ''}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}
