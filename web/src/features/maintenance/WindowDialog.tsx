import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useHosts } from '../../api/hooks/useHosts.ts';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { useCreateMaintenanceWindow, useUpdateMaintenanceWindow } from '../../api/hooks/useMaintenance.ts';
import type { MaintenanceWindow, MaintenanceWindowInput } from '../../api/types.ts';
import { fromLocalInput, toLocalInput } from './time.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

type TargetKind = 'org' | 'host' | 'inventory_group';
type WindowReason = 'maintenance' | 'silence';

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
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={w ? `Edit ${w.name}` : 'New window'} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="mw-name">Name</Label>
        <Input id="mw-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <Label htmlFor="mw-reason">Why</Label>
        <select id="mw-reason" className={SELECT} value={reason} onChange={(e) => setReason(e.target.value as WindowReason)}>
          <option value="maintenance">Maintenance — work is happening; scheduled runs can skip these hosts</option>
          <option value="silence">Silence — do not raise alerts; scheduled runs are unaffected</option>
        </select>
        <Label htmlFor="mw-kind">Applies to</Label>
        <select id="mw-kind" className={SELECT} value={kind} onChange={(e) => { setKind(e.target.value as TargetKind); setTargetId(''); }}>
          <option value="org">The whole organization</option>
          <option value="host">One host</option>
          <option value="inventory_group">An inventory group (and its subgroups)</option>
        </select>
        {kind === 'host' && (
          <>
            <Input aria-label="Find host" placeholder="Filter hosts by name" value={hostQuery} onChange={(e) => setHostQuery(e.target.value)} />
            <select aria-label="Host" className={SELECT} value={targetId} onChange={(e) => setTargetId(e.target.value)} required>
              <option value="">Choose a host…</option>
              {hostList.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </select>
          </>
        )}
        {kind === 'inventory_group' && (
          <select aria-label="Group" className={SELECT} value={targetId} onChange={(e) => setTargetId(e.target.value)} required>
            <option value="">Choose a group…</option>
            {groupList.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </select>
        )}
        <div role="radiogroup" aria-label="When" className="flex gap-1">
          {([false, true] as const).map((r) => (
            <button
              key={String(r)}
              type="button"
              role="radio"
              aria-checked={recurring === r}
              className={`rounded-md border px-2.5 py-1 text-xs ${recurring === r ? 'border-primary text-primary' : 'border-border text-muted-foreground'}`}
              onClick={() => setRecurring(r)}
            >
              {r ? 'Recurring' : 'Once'}
            </button>
          ))}
        </div>
        {recurring ? (
          <div className="grid grid-cols-3 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-cron">Starts (cron)</Label>
              <Input id="mw-cron" className="font-mono" value={cron} onChange={(e) => setCron(e.target.value)} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-duration">Lasts</Label>
              <Input id="mw-duration" className="font-mono" value={duration} onChange={(e) => setDuration(e.target.value)} placeholder="2h" />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-tz">Timezone</Label>
              <Input id="mw-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} />
            </div>
          </div>
        ) : (
          <div className="grid grid-cols-2 gap-2">
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-start">From</Label>
              <Input id="mw-start" type="datetime-local" value={startsAt} onChange={(e) => setStartsAt(e.target.value)} required />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor="mw-end">Until</Label>
              <Input id="mw-end" type="datetime-local" value={endsAt} onChange={(e) => setEndsAt(e.target.value)} required />
            </div>
          </div>
        )}
        <Label htmlFor="mw-note">Note</Label>
        <textarea id="mw-note" className="min-h-16 rounded-md border border-border bg-background p-2 text-xs" value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} />
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || update.isPending}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
