import { useState, type FormEvent } from 'react';
import { ArrowDown, ArrowUp, Plus, Trash2 } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { DateTimePicker } from '../../ui/DateTimePicker.tsx';
import { Label } from '../../ui/label.tsx';
import { fromLocalInput, toLocalInput } from '../maintenance/time.ts';
import type { OncallRotation, OncallRotationInput } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';

/** A rotation's channel, trimmed to what the picker needs — the same
 * shape RouteDialogChannel (RouteDialog.tsx) gives its own channel list. */
export interface RotationDialogChannel {
  id: string;
  name: string;
}

function defaultAnchor(): string {
  const now = new Date();
  now.setSeconds(0, 0);
  return toLocalInput(now.toISOString());
}

function nameOf(channels: RotationDialogChannel[], id: string): string {
  return channels.find((c) => c.id === id)?.name ?? id.slice(0, 8);
}

function moved<T>(list: T[], from: number, to: number): T[] {
  const copy = [...list];
  const [item] = copy.splice(from, 1);
  copy.splice(to, 0, item);
  return copy;
}

export interface RotationDialogProps {
  open: boolean;
  /** The rotation to edit; undefined creates one. */
  rotation?: OncallRotation;
  channels: RotationDialogChannel[];
  onClose: () => void;
  /** Saving is the caller's job (RotationsPage holds the create/update
   * mutations, since those need the organization and, on update, the
   * rotation's own revision for If-Match) — the same split RouteDialog
   * takes from AlertRoutesPage. */
  onSave?: (body: OncallRotationInput) => void | Promise<void>;
}

/**
 * RotationDialog creates or edits one on-call rotation: its name, period
 * (in days), anchor (the first handover — a date and a time together) and
 * timezone, plus an ordered channel cycle built with add/remove/reorder
 * rather than a multi-select — the order IS the cycle (oncall_dto.go's own
 * comment), so losing it on a reorder would silently reassign everyone's
 * week. Save stays disabled until a rotation has a name, a positive
 * period, an anchor and at least one channel in its cycle — the same shape
 * oncall.Validate enforces server-side.
 */
export function RotationDialog({ open, rotation, channels, onClose, onSave }: RotationDialogProps) {
  const t = useT();
  const m = t.notifications;
  const d = m.rotationDialog;
  const editing = !!rotation;
  const [name, setName] = useState(rotation?.name ?? '');
  const [periodDays, setPeriodDays] = useState(rotation?.period_days ?? 7);
  const [anchor, setAnchor] = useState(rotation?.anchor ? toLocalInput(rotation.anchor) : defaultAnchor());
  const [timezone, setTimezone] = useState(rotation?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const [cycle, setCycle] = useState<string[]>(rotation?.channels ?? []);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const available = channels.filter((c) => !cycle.includes(c.id));
  const valid = name.trim() !== '' && periodDays > 0 && anchor !== '' && cycle.length > 0;

  function add(id: string) {
    setCycle([...cycle, id]);
  }
  function remove(id: string) {
    setCycle(cycle.filter((c) => c !== id));
  }
  function moveUp(i: number) {
    if (i === 0) return;
    setCycle(moved(cycle, i, i - 1));
  }
  function moveDown(i: number) {
    if (i === cycle.length - 1) return;
    setCycle(moved(cycle, i, i + 1));
  }

  function body(): OncallRotationInput {
    return {
      name,
      period_days: periodDays,
      anchor: fromLocalInput(anchor),
      timezone,
      channels: cycle,
    };
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!valid) return;
    setError(null);
    setPending(true);
    try {
      await onSave?.(body());
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : m.savingFailed);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={editing ? d.editTitle(rotation.name) : d.newTitle} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="rot-name">{m.name}</Label>
        <Input id="rot-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <div className="grid grid-cols-3 gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="rot-period">{d.periodDays}</Label>
            <Input
              id="rot-period"
              type="number"
              min={1}
              value={periodDays}
              onChange={(e) => setPeriodDays(Number(e.target.value))}
              required
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rot-anchor">{d.anchor}</Label>
            <DateTimePicker id="rot-anchor" value={anchor} onChange={setAnchor} required />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="rot-tz">{m.timezone}</Label>
            <Input id="rot-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} required />
          </div>
        </div>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.cycle}</legend>
          <div className="flex flex-col gap-1">
            {cycle.map((id, i) => (
              <div key={id} className="flex items-center gap-2 text-xs">
                <span className="w-5 text-muted-foreground">{i + 1}.</span>
                <span className="flex-1">{nameOf(channels, id)}</span>
                <Button type="button" variant="ghost" aria-label={d.moveUp(nameOf(channels, id))} disabled={i === 0} onClick={() => moveUp(i)}>
                  <ArrowUp size={13} />
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  aria-label={d.moveDown(nameOf(channels, id))}
                  disabled={i === cycle.length - 1}
                  onClick={() => moveDown(i)}
                >
                  <ArrowDown size={13} />
                </Button>
                <Button type="button" variant="ghost" aria-label={d.removeAria(nameOf(channels, id))} onClick={() => remove(id)}>
                  <Trash2 size={13} />
                </Button>
              </div>
            ))}
            {cycle.length === 0 && <span className="text-xs text-muted-foreground">{d.addAtLeastOne}</span>}
          </div>
          <div className="flex flex-wrap gap-2">
            {available.map((c) => (
              <Button key={c.id} type="button" variant="ghost" onClick={() => add(c.id)}>
                <Plus size={13} /> {d.add(c.name)}
              </Button>
            ))}
            {channels.length === 0 && <span className="text-xs text-muted-foreground">{m.addChannelFirst}</span>}
          </div>
        </fieldset>
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={!valid || pending}>
            {t.common.save}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
