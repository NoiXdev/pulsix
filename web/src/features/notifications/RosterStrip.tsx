import { BellOff } from 'lucide-react';
import { Pill } from '../../ui/badge.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

/** One segment of a rotation's roster, trimmed to what the strip needs to
 * render — the full OncallShift DTO also carries channel_id, which this
 * view never resolves or links anywhere. */
export interface RosterStripShift {
  starts_at: string;
  ends_at: string;
  channel_name: string;
  stand_in: boolean;
}

/**
 * RosterStrip shows a rotation's roster as a horizontal band of shifts:
 * who is on call now, and when stand-ins (exceptions) replace the cycle's
 * own answer. An empty strip is never rendered bare — a blank band reads
 * as "not loaded yet", not as "alerts routed here reach no one", so the
 * empty case says so in words instead, the same fact ReachesNobody
 * (AlertRouteCells.tsx) states for a channel-less route.
 *
 * That empty case is a floor this component holds on its own, not one its
 * current caller can reach: oncall.Shifts always tiles the range it is
 * given with at least one segment, so a rotation that cannot answer who
 * is on call arrives at RotationsPage as a FAILED REQUEST instead, and
 * that page says so itself (whole-branch review, Important #4).
 */
export function RosterStrip({ shifts, now }: { shifts: RosterStripShift[]; now: string }) {
  const t = useT();
  const fmt = useFormat();
  const when = (iso: string) => fmt.dateTime(iso, { dateStyle: 'short', timeStyle: 'short' });
  if (shifts.length === 0) {
    return (
      <p className="flex items-center gap-1.5 text-xs font-semibold text-destructive">
        <BellOff size={13} aria-hidden="true" />
        {t.notifications.roster.nobody}
      </p>
    );
  }

  const nowMs = new Date(now).getTime();

  return (
    <div className="flex gap-1.5 overflow-x-auto">
      {shifts.map((s, i) => {
        const current = nowMs >= new Date(s.starts_at).getTime() && nowMs < new Date(s.ends_at).getTime();
        return (
          <div
            key={i}
            className={`flex min-w-[150px] flex-col gap-1 rounded-md border p-2 text-xs ${current ? 'border-primary bg-primary/10' : 'border-border'}`}
          >
            <span className="flex items-center gap-1.5 font-medium">
              <span>{s.channel_name}</span>
              {s.stand_in && <Pill tone="accent">{t.notifications.roster.standIn}</Pill>}
            </span>
            <span className="text-muted-foreground">
              {when(s.starts_at)} – {when(s.ends_at)}
            </span>
            {current && <span className="font-semibold text-primary">{t.notifications.roster.onCallNow}</span>}
          </div>
        );
      })}
    </div>
  );
}
