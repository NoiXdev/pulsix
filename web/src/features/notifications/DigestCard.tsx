import { Input } from '../../ui/input.tsx';
import { TimeField } from '../../ui/TimeField.tsx';
import { CardSaveBar, SettingsCard, SettingsRow } from '../../ui/settings.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCardForm } from '../../ui/useCardForm.ts';
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
  return <DigestForm org={org} channels={channels} canWrite={canWrite} initial={d} />;
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
  // Channel ids stay sorted so that picking and unpicking one is not a change.
  const form = useCardForm({
    enabled: initial.enabled,
    weekday: initial.weekday,
    time: initial.time,
    timezone: initial.timezone,
    channels: (initial.channels ?? []).map((c) => c.id).sort(),
  });
  const { draft, dirty } = form;
  const set = (patch: Partial<typeof draft>) => form.setDraft((d) => ({ ...d, ...patch }));

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!canWrite) return;
    try {
      await save.mutateAsync(draft);
      form.markSaved();
      toast.show(m.saved);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.saveFailed, 'destructive');
    }
  }

  return (
    <form id="digest-form" onSubmit={(e) => void onSubmit(e)}>
      <SettingsCard
        title={m.title}
        description={m.description}
        badge={initial.next_run_at && <span className="text-xs text-muted-foreground">{m.next(nextRunLabel(initial.next_run_at, initial.timezone, fmt))}</span>}
        footer={canWrite ? <CardSaveBar formId="digest-form" dirty={dirty} saving={save.isPending} onDiscard={form.discard} saveLabel={m.save} /> : null}
      >
        <SettingsRow label={m.send} htmlFor="digest-enabled">
          <Switch id="digest-enabled" checked={draft.enabled} onCheckedChange={(enabled) => set({ enabled })} disabled={!canWrite} />
        </SettingsRow>
        <SettingsRow label={m.day} htmlFor="digest-day">
          <div className="w-44 max-w-full">
            <Select
              id="digest-day"
              portal
              options={DAYS.map((d) => ({ value: d, label: t.notifications.weekdayLong[d] }))}
              value={draft.weekday}
              onChange={(v) => set({ weekday: v as DigestInput['weekday'] })}
              disabled={!canWrite}
            />
          </div>
        </SettingsRow>
        <SettingsRow label={m.time} htmlFor="digest-time">
          <TimeField id="digest-time" value={draft.time} onChange={(time) => set({ time })} disabled={!canWrite} />
        </SettingsRow>
        <SettingsRow label={t.notifications.timezone} htmlFor="digest-tz">
          <Input id="digest-tz" className="w-60 max-w-full font-mono" value={draft.timezone} onChange={(e) => set({ timezone: e.target.value })} disabled={!canWrite} />
        </SettingsRow>
        <SettingsRow label={m.sendTo} stack>
          <div className="flex flex-wrap gap-3">
            {channels.map((c) => (
              <label key={c.id} className="flex items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={draft.channels.includes(c.id)}
                  onChange={() => set({ channels: draft.channels.includes(c.id) ? draft.channels.filter((x) => x !== c.id) : [...draft.channels, c.id].sort() })}
                  disabled={!canWrite}
                />
                {c.name}
              </label>
            ))}
            {channels.length === 0 && <span className="text-xs text-muted-foreground">{m.addChannelFirst}</span>}
          </div>
        </SettingsRow>
      </SettingsCard>
    </form>
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
