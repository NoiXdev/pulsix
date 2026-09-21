import { useState } from 'react';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { type NotifyRuleInput, useChannels, useScheduleNotifications, useSetScheduleNotifications } from '../../api/hooks/useNotifications.ts';

type Category = NotifyRuleInput['on'];
const CATEGORIES: Category[] = ['failure', 'change', 'success', 'skipped'];
const LABEL: Record<Category, string> = { failure: 'on failure', change: 'on change', success: 'on success', skipped: 'skipped / missed' };
const TONE: Record<Category, Tone> = { failure: 'crit', change: 'warn', success: 'ok', skipped: 'unknown' };

type Matrix = Record<string, Category[]>;

/** ScheduleNotifications edits a schedule's notify rules (spec 2026-09-18
 * §7) as a channel × category matrix — the stored shape: each channel gets
 * at most one message per run, whichever categories it listens to.
 * Readers see the rules as text. */
export function ScheduleNotifications({ org, scheduleId, canWrite }: { org: string; scheduleId: string; canWrite: boolean }) {
  const rules = useScheduleNotifications(org, scheduleId);
  const channels = useChannels(org, canWrite);
  // Backlog 2.85: a failed rules query used to `return null` — the whole
  // section vanished with no hint anything went wrong, the same class of
  // silent failure the calendar's empty grid was. The guard renders the
  // error state in the section's place. The channel-list failure keeps
  // the bespoke reader-view surface below: the rules stay readable while
  // only the editing matrix is missing.
  const guard = useQueryGuard(rules);
  if (guard) return guard;
  if (!rules.data) return null;
  const list = rules.data.rules ?? [];
  if (!canWrite || !channels.data) {
    // An editor whose channel list failed to load would otherwise see the
    // reader's view with no hint why the matrix is missing.
    return (
      <section aria-label="Notifications" className="flex flex-col gap-1.5">
        <span className="text-[11px] font-medium text-muted-foreground">Notifications</span>
        {canWrite && channels.isError && (
          <p role="alert" className="text-xs text-destructive">
            The channels could not be loaded, so these rules cannot be edited right now.
          </p>
        )}
        {list.length === 0 && <p className="text-xs text-muted-foreground">No notifications — runs appear in the activity feed only.</p>}
        {list.map((r) => (
          <div key={r.on} className="flex items-center gap-2 text-xs">
            <Pill tone={TONE[r.on as Category]}>{LABEL[r.on as Category]}</Pill>
            <span>→ {(r.channels ?? []).map((c) => c.name).join(', ')}</span>
          </div>
        ))}
      </section>
    );
  }
  const initial: Matrix = {};
  for (const r of list) for (const c of r.channels ?? []) initial[c.id] = [...(initial[c.id] ?? []), r.on as Category];
  return <RuleMatrix key={JSON.stringify(initial)} org={org} scheduleId={scheduleId} initial={initial} channels={channels.data.channels ?? []} />;
}

function RuleMatrix({ org, scheduleId, initial, channels }: { org: string; scheduleId: string; initial: Matrix; channels: { id: string; name: string }[] }) {
  const toast = useToast();
  const save = useSetScheduleNotifications(org, scheduleId);
  const [matrix, setMatrix] = useState<Matrix>(initial);

  function toggle(channel: string, cat: Category) {
    const cur = matrix[channel] ?? [];
    setMatrix({ ...matrix, [channel]: cur.includes(cat) ? cur.filter((c) => c !== cat) : [...cur, cat] });
  }

  async function onSave() {
    const body: NotifyRuleInput[] = CATEGORIES.map((on) => ({ on, channels: channels.filter((c) => (matrix[c.id] ?? []).includes(on)).map((c) => c.id) })).filter(
      (r) => r.channels.length > 0,
    );
    try {
      await save.mutateAsync(body);
      toast.show('Notifications saved');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Saving notifications failed', 'destructive');
    }
  }

  return (
    <section aria-label="Notifications" className="flex flex-col gap-2">
      <span className="text-[11px] font-medium text-muted-foreground">Notifications</span>
      {channels.length === 0 ? (
        <p className="text-xs text-muted-foreground">No channels yet — admins add them under Settings → Notifications.</p>
      ) : (
        <table className="text-xs">
          <thead>
            <tr>
              <th className="text-left font-medium text-muted-foreground">Channel</th>
              {CATEGORIES.map((c) => (
                <th key={c} className="px-1 font-medium">
                  <Pill tone={TONE[c]}>{LABEL[c]}</Pill>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {channels.map((ch) => (
              <tr key={ch.id}>
                <td className="truncate py-1">{ch.name}</td>
                {CATEGORIES.map((c) => (
                  <td key={c} className="text-center">
                    <input type="checkbox" aria-label={`${ch.name} ${LABEL[c]}`} checked={(matrix[ch.id] ?? []).includes(c)} onChange={() => toggle(ch.id, c)} />
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <Button type="button" className="self-end" onClick={() => void onSave()} disabled={save.isPending || channels.length === 0}>
        Save notifications
      </Button>
    </section>
  );
}
