import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { TimeField } from '../../ui/TimeField.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { type DigestInput, useDigest, useSetDigest } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel } from '../../api/types.ts';
import { type Format, useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

const DAYS: DigestInput['weekday'][] = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];

/** DigestCard is the weekly digest (spec 2026-09-18 §7): runs per
 * schedule, failures, skips and misses, and the slowest runs of the past
 * seven days, sent to the chosen channels on one weekday. */
export function DigestCard({ org, channels, canWrite }: { org: string; channels: NotifyChannel[]; canWrite: boolean }) {
  const digest = useDigest(org);
  // Backlog 2.85: the card used to `return null` while the digest query
  // failed, so the section silently vanished with no hint anything went
  // wrong — the same class of silent failure the calendar's empty grid
  // was. The guard renders the error state in the section's place; a
  // refetch that succeeds brings the form back.
  const guard = useQueryGuard(digest);
  if (guard) return guard;
  if (!digest.data) return null;
  const d = digest.data.digest;
  return <DigestForm key={JSON.stringify(d)} org={org} channels={channels} canWrite={canWrite} initial={d} />;
}

interface DigestFormProps {
  org: string;
  channels: NotifyChannel[];
  canWrite: boolean;
  initial: { enabled: boolean; weekday: DigestInput['weekday']; time: string; timezone: string; channels: { id: string }[] | null; next_run_at?: string };
}

function DigestForm({ org, channels, canWrite, initial }: DigestFormProps) {
  const t = useT();
  const fmt = useFormat();
  const m = t.notifications.digest;
  const toast = useToast();
  const save = useSetDigest(org);
  const [enabled, setEnabled] = useState(initial.enabled);
  const [weekday, setWeekday] = useState(initial.weekday);
  const [time, setTime] = useState(initial.time);
  const [timezone, setTimezone] = useState(initial.timezone);
  const [selected, setSelected] = useState<string[]>((initial.channels ?? []).map((c) => c.id));

  async function onSave() {
    try {
      await save.mutateAsync({ enabled, weekday, time, timezone, channels: selected });
      toast.show(m.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.saveFailed, 'destructive');
    }
  }

  return (
    <section aria-label={m.title} className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold">{m.title}</h2>
        {initial.next_run_at && <span className="text-xs text-muted-foreground">{m.next(nextRunLabel(initial.next_run_at, initial.timezone, fmt))}</span>}
      </div>
      <p className="text-xs text-muted-foreground">{m.description}</p>
      <div className="flex items-center gap-2 text-xs">
        <Switch id="digest-enabled" checked={enabled} onCheckedChange={setEnabled} disabled={!canWrite} />
        <label htmlFor="digest-enabled">{m.send}</label>
      </div>
      <div className="grid grid-cols-3 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-day">{m.day}</Label>
          <Select
            id="digest-day"
            options={DAYS.map((d) => ({ value: d, label: t.notifications.weekdayLong[d] }))}
            value={weekday}
            onChange={(v) => setWeekday(v as DigestInput['weekday'])}
            disabled={!canWrite}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-time">{m.time}</Label>
          <TimeField id="digest-time" value={time} onChange={setTime} disabled={!canWrite} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-tz">{t.notifications.timezone}</Label>
          <Input id="digest-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} disabled={!canWrite} />
        </div>
      </div>
      <span className="text-[11px] font-medium text-muted-foreground">{m.sendTo}</span>
      <div className="flex flex-wrap gap-3">
        {channels.map((c) => (
          <label key={c.id} className="flex items-center gap-1.5 text-xs">
            <input
              type="checkbox"
              checked={selected.includes(c.id)}
              onChange={() => setSelected(selected.includes(c.id) ? selected.filter((x) => x !== c.id) : [...selected, c.id])}
              disabled={!canWrite}
            />
            {c.name}
          </label>
        ))}
        {channels.length === 0 && <span className="text-xs text-muted-foreground">{m.addChannelFirst}</span>}
      </div>
      {canWrite && (
        <Button type="button" className="self-end" onClick={() => void onSave()} disabled={save.isPending}>
          {m.save}
        </Button>
      )}
    </section>
  );
}

/** nextRunLabel writes the digest's next send in the digest's OWN zone,
 * named (backlog 2.88). It used to render in the browser's zone with
 * nothing saying so, directly above a Timezone field reading something
 * else: an operator who set Monday 08:00 in Auckland saw "Sunday,
 * 21:00" on a Berlin screen and could not tell whether the setting had
 * taken.
 *
 * A zone the browser does not know throws out of toLocaleString, which
 * would take the whole card down over a label. The server resolves the
 * name against its own database, and the two can disagree — a zone
 * added or renamed in one and not the other — so the fallback shows the
 * browser's zone and says that is what it is, rather than leaving the
 * operator with no line at all. */
function nextRunLabel(at: string, timezone: string, fmt: Format): string {
  try {
    return `${fmt.dateTime(at, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short' })} (${timezone})`;
  } catch {
    const local = Intl.DateTimeFormat().resolvedOptions().timeZone;
    return `${fmt.dateTime(at, { dateStyle: 'medium', timeStyle: 'short' })} (${local})`;
  }
}
