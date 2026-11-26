import { Pill } from '../../ui/badge.tsx';
import type { Schedule } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/** ScheduleStatusPill is a schedule's state: active, paused, or paused
 * because an assistant changed it and a person has to review that (spec
 * 2026-11-25 C2). */
export function ScheduleStatusPill({ schedule }: { schedule: Pick<Schedule, 'paused' | 'paused_reason'> }) {
  const t = useT().schedules;
  if (schedule.paused_reason) return <Pill tone="warn">{t.needsReview}</Pill>;
  if (schedule.paused) return <Pill tone="unknown">{t.paused}</Pill>;
  return <Pill tone="ok">{t.active}</Pill>;
}
