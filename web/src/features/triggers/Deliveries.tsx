import { Pill, type Tone } from '../../ui/badge.tsx';
import { outcomeLabel } from './outcome.ts';
import { useTriggerDeliveries, useTriggerPending } from '../../api/hooks/useTriggers.ts';
import type { TriggerDelivery } from '../../api/types.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { RunGroupLink } from './RunGroupLink.tsx';

const TONE: Record<string, Tone> = { triggered: 'ok', skipped_active: 'unknown', rate_limited: 'warn', invalid_payload: 'warn', disabled: 'unknown', failed: 'crit', collected: 'unknown' };

/** waiting reports whether a collected delivery may still be in the
 * trigger's pending run: nothing has become of it yet. */
const waiting = (d: TriggerDelivery) => d.outcome === 'collected' && !d.job_group_id && !d.superseded_by && !!d.pending_id;

/** Deliveries is a trigger's delivery log: time, source, outcome, reason
 * (rejections at most one row per minute; request bodies are never kept).
 * A collected delivery (backlog 2.137) says when its run starts while it
 * waits, and links the run it collapsed into, or the run that superseded
 * it. */
export function Deliveries({ org, id }: { org: string; id: string }) {
  const t = useT();
  const m = t.triggers.delivery;
  const fmt = useFormat();
  const list = useTriggerDeliveries(org, id).data?.deliveries ?? [];
  const pending = useTriggerPending(org, id, list.some(waiting)).data?.pending;
  if (list.length === 0) return <p className="text-xs text-muted-foreground">{t.triggers.deliveriesNone}</p>;
  return (
    <ul className="flex flex-col gap-1">
      {list.map((d) => {
        const collapsed = d.outcome === 'collected' && d.job_group_id;
        const superseded = d.outcome === 'collected' && !d.job_group_id && d.superseded_by;
        // The link says what the superseded note says; the rest of the
        // reason stays.
        const reason = superseded ? (d.reason ?? '').replace(`superseded by run ${d.superseded_by}`, '').replace(/^[;\s]+|[;\s]+$/g, '') : d.reason;
        return (
          <li key={d.id} className="flex flex-wrap items-center gap-2 text-xs">
            <Pill tone={collapsed ? 'ok' : (TONE[d.outcome] ?? 'crit')}>{outcomeLabel(d.outcome, t)}</Pill>
            <span className="text-muted-foreground">{fmt.dateTime(d.at)}</span>
            {d.source_ip && <span className="font-mono">{d.source_ip}</span>}
            {collapsed && (
              <span className="flex items-center gap-1">
                <span>{m.collapsed}</span>
                <RunGroupLink org={org} group={d.job_group_id as string} />
              </span>
            )}
            {superseded && (
              <span className="flex items-center gap-1">
                <span>{m.superseded}</span>
                <RunGroupLink org={org} group={d.superseded_by as string} />
              </span>
            )}
            {waiting(d) && pending && pending.id === d.pending_id && <span>{m.waiting(fmt.dateTime(pending.due_at))}</span>}
            {reason && <span className="truncate text-muted-foreground">{reason}</span>}
          </li>
        );
      })}
    </ul>
  );
}
