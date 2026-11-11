import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { DateTimePicker } from '../../ui/DateTimePicker.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useT } from '../../i18n/useT.ts';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useCreateMaintenanceWindow, useUpdateMaintenanceWindow } from '../../api/hooks/useMaintenance.ts';
import type { MaintenanceWindow, MaintenanceWindowInput } from '../../api/types.ts';
import { fromLocalInput, toLocalInput } from './time.ts';

type TargetKind = 'org' | 'host' | 'inventory_group';
type WindowReason = 'maintenance' | 'silence';

const REASONS: WindowReason[] = ['maintenance', 'silence'];
const TARGET_KINDS: TargetKind[] = ['org', 'host', 'inventory_group'];

/** WindowDialog creates or edits a maintenance window: its reason, its
 * target (the organization, a host or an inventory group) and either a
 * one-off stretch or a recurring timetable.
 *
 * The reason is a field here, not merely something the API accepts (v0.4a
 * final review, Important). v0.4a decision 3 makes silencing the face of a
 * maintenance window rather than a second subsystem, and claims it
 * therefore "reuses the API and GUI" — which was true of the API and not
 * of the GUI: `body()` omitted `reason`, `MaintenanceWindowBody.Reason` is
 * omitempty, and the service defaults an empty one to `maintenance`. So
 * any save from this dialog silently converted a silence window back into
 * a maintenance one, un-muting every alert it covered and (since the
 * schedules' guard reads maintenance windows) newly pausing that host's
 * scheduled runs, with nothing on screen to say either had happened. */
export function WindowDialog({ org, window: w, onClose }: { org: string; window?: MaintenanceWindow; onClose: () => void }) {
  const t = useT();
  const create = useCreateMaintenanceWindow(org);
  const update = useUpdateMaintenanceWindow(org, w?.id ?? '');
  const [name, setName] = useState(w?.name ?? '');
  const [kind, setKind] = useState<TargetKind>((w?.target_kind as TargetKind) ?? 'org');
  const [targetId, setTargetId] = useState(w?.target_id ?? '');
  const [hostQuery, setHostQuery] = useState('');
  const [recurring, setRecurring] = useState(!!w?.cron);
  const [startsAt, setStartsAt] = useState(toLocalInput(w?.starts_at));
  const [endsAt, setEndsAt] = useState(toLocalInput(w?.ends_at));
  const [cron, setCron] = useState(w?.cron ?? '0 2 * * 6');
  const [duration, setDuration] = useState(w?.duration ?? '2h');
  const [timezone, setTimezone] = useState(w?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const [note, setNote] = useState(w?.note ?? '');
  const [reason, setReason] = useState<WindowReason>((w?.reason as WindowReason) ?? 'maintenance');
  const [error, setError] = useState<string | null>(null);
  const hosts = useHosts(org, { managed: true, q: hostQuery || undefined });
  const groups = useGroups(org);
  const hostList = hosts.data?.pages.flatMap((p) => p.hosts ?? []) ?? [];
  const groupList = groups.data?.groups ?? [];

  function body(): MaintenanceWindowInput {
    const base = { name, target_kind: kind, ...(kind !== 'org' && targetId ? { target_id: targetId } : {}), note, reason };
    return recurring
      ? { ...base, cron, duration, timezone }
      : { ...base, starts_at: startsAt ? fromLocalInput(startsAt) : undefined, ends_at: endsAt ? fromLocalInput(endsAt) : undefined };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      if (w) await update.mutateAsync({ ...body(), revision: w.revision });
      else await create.mutateAsync(body());
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : t.maintenance.dialog.saveFailed);
    }
  }

  return (
    <Dialog open onClose={onClose} title={w ? t.maintenance.dialog.editTitle(w.name) : t.maintenance.dialog.newTitle} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="mw-name">{t.maintenance.dialog.name}</Label>
        <Input id="mw-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <Label htmlFor="mw-reason">{t.maintenance.dialog.why}</Label>
        <Select
          id="mw-reason"
          options={REASONS.map((r) => ({ value: r, label: t.maintenance.dialog.reasons[r] }))}
          value={reason}
          onChange={(value) => setReason(value as WindowReason)}
        />
        <Label htmlFor="mw-kind">{t.maintenance.dialog.appliesTo}</Label>
        <Select
          id="mw-kind"
          options={TARGET_KINDS.map((k) => ({ value: k, label: t.maintenance.dialog.targetKinds[k] }))}
          value={kind}
          onChange={(value) => { setKind(value as TargetKind); setTargetId(''); }}
        />
        {kind === 'host' && (
          <>
            <Input aria-label={t.maintenance.dialog.findHost} placeholder={t.maintenance.dialog.findHostPlaceholder} value={hostQuery} onChange={(e) => setHostQuery(e.target.value)} />
            <Select
              aria-label={t.maintenance.dialog.host}
              options={hostList.map((h) => ({ value: h.id, label: h.name }))}
              value={targetId}
              onChange={setTargetId}
              placeholder={t.maintenance.dialog.chooseHost}
              required
            />
          </>
        )}
        {kind === 'inventory_group' && (
          <Select
            aria-label={t.maintenance.dialog.group}
            options={groupList.map((g) => ({ value: g.id, label: g.name }))}
            value={targetId}
            onChange={setTargetId}
            placeholder={t.maintenance.dialog.chooseGroup}
            required
          />
        )}
        <div role="radiogroup" aria-label={t.maintenance.dialog.when} className="flex gap-1">
          {([false, true] as const).map((r) => (
            <button
              key={String(r)}
              type="button"
              role="radio"
              aria-checked={recurring === r}
              className={`rounded-md border px-2.5 py-1 text-xs ${recurring === r ? 'border-primary text-primary' : 'border-border text-muted-foreground'}`}
              onClick={() => setRecurring(r)}
            >
              {r ? t.maintenance.dialog.recurring : t.maintenance.dialog.once}
            </button>
          ))}
        </div>
        {recurring ? (
          <div className="grid grid-cols-3 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-cron">{t.maintenance.dialog.cron}</Label>
              <Input id="mw-cron" className="font-mono" value={cron} onChange={(e) => setCron(e.target.value)} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-duration">{t.maintenance.dialog.lasts}</Label>
              <Input id="mw-duration" className="font-mono" value={duration} onChange={(e) => setDuration(e.target.value)} placeholder="2h" />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-tz">{t.maintenance.dialog.timezone}</Label>
              <Input id="mw-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} />
            </div>
          </div>
        ) : (
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-start">{t.maintenance.dialog.from}</Label>
              <DateTimePicker id="mw-start" value={startsAt} onChange={setStartsAt} required />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-end">{t.maintenance.dialog.until}</Label>
              <DateTimePicker id="mw-end" value={endsAt} onChange={setEndsAt} required />
            </div>
          </div>
        )}
        <Label htmlFor="mw-note">{t.maintenance.dialog.note}</Label>
        <textarea id="mw-note" className="min-h-16 rounded-md border border-border bg-background p-2 text-xs" value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} />
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            {t.common.save}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
