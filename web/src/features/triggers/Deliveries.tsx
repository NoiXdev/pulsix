import { Pill, type Tone } from '../../ui/badge.tsx';
import { outcomeLabel } from './outcome.ts';
import { useTriggerDeliveries } from '../../api/hooks/useTriggers.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

const TONE: Record<string, Tone> = { triggered: 'ok', skipped_active: 'unknown', rate_limited: 'warn', invalid_payload: 'warn', disabled: 'unknown', failed: 'crit' };

/** Deliveries is a trigger's delivery log: time, source, outcome, reason
 * (rejections at most one row per minute; request bodies are never kept). */
export function Deliveries({ org, id }: { org: string; id: string }) {
  const t = useT();
  const fmt = useFormat();
  const list = useTriggerDeliveries(org, id).data?.deliveries ?? [];
  if (list.length === 0) return <p className="text-xs text-muted-foreground">{t.triggers.deliveriesNone}</p>;
  return (
    <ul className="flex flex-col gap-1">
      {list.map((d) => (
        <li key={d.id} className="flex items-center gap-2 text-xs">
          <Pill tone={TONE[d.outcome] ?? 'crit'}>{outcomeLabel(d.outcome, t)}</Pill>
          <span className="text-muted-foreground">{fmt.dateTime(d.at)}</span>
          {d.source_ip && <span className="font-mono">{d.source_ip}</span>}
          {d.reason && <span className="truncate text-muted-foreground">{d.reason}</span>}
        </li>
      ))}
    </ul>
  );
}
