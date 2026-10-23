import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useChannelDeliveries, useDeliveryPayload, useResendDelivery } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel } from '../../api/types.ts';

const STATUS_TONE: Record<string, Tone> = { sent: 'ok', pending: 'warn', failed: 'crit' };

/** DeliveryLog shows a channel's newest deliveries: what was sent, how
 * often it was tried, and the last error of a retried or failed one.
 * A row opens to show the event the message was built from, and a
 * failed one can be put back on the queue (backlog 2.75). */
export function DeliveryLog({ org, channel, canWrite, onClose }: { org: string; channel: NotifyChannel; canWrite: boolean; onClose: () => void }) {
  const log = useChannelDeliveries(org, channel.id);
  const [open, setOpen] = useState<string | null>(null);
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
            <li key={d.id} className="flex flex-col gap-2 py-2" data-delivery>
              <div className="grid grid-cols-[140px_130px_70px_minmax(0,1fr)_auto] items-center gap-3 text-xs">
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
                <span className="flex gap-1">
                  <Button
                    type="button"
                    variant="ghost"
                    aria-expanded={open === d.id}
                    onClick={() => setOpen(open === d.id ? null : d.id)}
                  >
                    {open === d.id ? 'Hide message' : 'Show message'}
                  </Button>
                  {/* Only a failed delivery: a pending one is already on
                      its way, and a sent one would put a second copy in
                      front of a receiver that has it. */}
                  {canWrite && d.status === 'failed' && <ResendButton org={org} channel={channel.id} delivery={d.id} />}
                </span>
              </div>
              {open === d.id && <Payload org={org} channel={channel.id} delivery={d.id} />}
            </li>
          ))}
        </ul>
      )}
    </Dialog>
  );
}

/** Payload shows the event the message was built from — the input every
 * channel's body, custom template or not, was rendered from. */
function Payload({ org, channel, delivery }: { org: string; channel: string; delivery: string }) {
  const one = useDeliveryPayload(org, channel, delivery);
  if (!one.data) return <QueryLoading />;
  return (
    <pre
      aria-label="Message sent"
      className="max-h-64 overflow-auto rounded-md border border-border bg-muted/40 p-2 font-mono text-[11px] leading-relaxed"
    >
      {JSON.stringify(one.data.payload, null, 2)}
    </pre>
  );
}

function ResendButton({ org, channel, delivery }: { org: string; channel: string; delivery: string }) {
  const toast = useToast();
  const resend = useResendDelivery(org, channel);
  async function go() {
    try {
      await resend.mutateAsync(delivery);
      toast.show('Queued to be sent again');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Sending it again failed', 'destructive');
    }
  }
  return (
    <Button type="button" onClick={() => void go()} disabled={resend.isPending}>
      Send again
    </Button>
  );
}
