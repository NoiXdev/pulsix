import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { type DigestInput, useDigest, useSetDigest } from '../../api/hooks/useNotifications.ts';
import type { NotifyChannel } from '../../api/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';
const DAYS: DigestInput['weekday'][] = ['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'];
const DAY_LABEL: Record<DigestInput['weekday'], string> = { mon: 'Monday', tue: 'Tuesday', wed: 'Wednesday', thu: 'Thursday', fri: 'Friday', sat: 'Saturday', sun: 'Sunday' };

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
      toast.show('Weekly digest saved');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Saving the digest failed', 'destructive');
    }
  }

  return (
    <section aria-label="Weekly digest" className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold">Weekly digest</h2>
        {initial.next_run_at && <span className="text-xs text-muted-foreground">next: {new Date(initial.next_run_at).toLocaleString()}</span>}
      </div>
      <p className="text-xs text-muted-foreground">Runs per schedule, failures, skips and misses, and the slowest runs of the past seven days.</p>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} disabled={!canWrite} />
        Send the weekly digest
      </label>
      <div className="grid grid-cols-3 gap-2">
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-day">Day</Label>
          <select id="digest-day" className={SELECT} value={weekday} onChange={(e) => setWeekday(e.target.value as DigestInput['weekday'])} disabled={!canWrite}>
            {DAYS.map((d) => (
              <option key={d} value={d}>
                {DAY_LABEL[d]}
              </option>
            ))}
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-time">Time</Label>
          <Input id="digest-time" type="time" value={time} onChange={(e) => setTime(e.target.value)} disabled={!canWrite} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="digest-tz">Timezone</Label>
          <Input id="digest-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} disabled={!canWrite} />
        </div>
      </div>
      <span className="text-[11px] font-medium text-muted-foreground">Send to</span>
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
        {channels.length === 0 && <span className="text-xs text-muted-foreground">Add a channel first.</span>}
      </div>
      {canWrite && (
        <Button type="button" className="self-end" onClick={() => void onSave()} disabled={save.isPending}>
          Save digest
        </Button>
      )}
    </section>
  );
}
