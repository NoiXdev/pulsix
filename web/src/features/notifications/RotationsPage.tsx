import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { BellOff, CalendarClock, Plus, Trash2 } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { DateTimePicker } from '../../ui/DateTimePicker.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useChannels } from '../../api/hooks/useNotifications.ts';
import {
  useCreateOncallException,
  useCreateOncallRotation,
  useDeleteOncallException,
  useDeleteOncallRotation,
  useOncallExceptions,
  useOncallRotations,
  useOncallShifts,
  useUpdateOncallRotation,
} from '../../api/hooks/useOncall.ts';
import { may } from '../../lib/roles.ts';
import { fromLocalInput, toLocalInput } from '../maintenance/time.ts';
import type { OncallRotation, OncallRotationInput } from '../../api/types.ts';
import { RotationDialog } from './RotationDialog.tsx';
import { RosterStrip } from './RosterStrip.tsx';
import { ChannelDialog } from './ChannelDialog.tsx';
import { useT } from '../../i18n/useT.ts';
import { useFormat } from '../../i18n/format.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS = 'minmax(0, 0.9fr) 90px minmax(0, 1.4fr) 160px';

// The roster strip only needs a near-term window to answer "who is on
// call now, and next" — two weeks is comfortably past any period this
// feature is likely configured with, without asking Roster to compute an
// unbounded range.
const ROSTER_DAYS = 14;

function rosterRange(): { from: string; to: string } {
  const from = new Date();
  return { from: from.toISOString(), to: new Date(from.getTime() + ROSTER_DAYS * 86_400_000).toISOString() };
}

/** RotationsPage is Settings → On-call rotations (v0.4c design): the
 * organization's rotations, each with an ordered channel cycle, and —
 * beneath the list — every rotation's current roster plus its stand-ins
 * (exceptions). Same permission bar as notification channels and alert
 * routes (oncall_handlers.go's own comment: a rotation decides where
 * alert data leaves the system, the same weight those two already carry)
 * — operators read, admins manage.
 *
 * Reachable from the Alerting group of the Settings side navigation
 * (SettingsLayout.tsx) and from RouteDialog's "On-call rotation" channel
 * source — before this page existed, that radio option had nothing
 * feeding it (task-10-brief.md's own context note).
 */
export function RotationsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const m = t.notifications;
  const r8 = m.rotations;
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const role = orgInfo.data?.org.role;
  const canRead = may(role, 'channels.read');
  const canWrite = may(role, 'channels.write');
  const rotations = useOncallRotations(org, !!orgInfo.data && canRead);
  const channels = useChannels(org, !!orgInfo.data && canRead);
  const create = useCreateOncallRotation(org);
  const update = useUpdateOncallRotation(org);
  const remove = useDeleteOncallRotation(org);
  const [editing, setEditing] = useState<OncallRotation | 'new' | null>(null);

  const guard = useQueryGuard(orgInfo);
  const rotationsGuard = useQueryGuard(rotations);
  if (guard) return guard;
  if (rotationsGuard) return rotationsGuard;
  if (!orgInfo.data) return <QueryLoading />;
  const header = (
    <h1 className="text-xl font-semibold">{m.title}</h1>
  );
  if (!canRead) {
    return (
      <>
        {header}
        <p className="text-sm text-muted-foreground">{r8.noAccess}</p>
      </>
    );
  }
  if (!rotations.data) return <QueryLoading />;
  const list = rotations.data.rotations ?? [];
  const channelList = channels.data?.channels ?? [];
  const channelOptions = channelList.map((c) => ({ id: c.id, name: c.name }));

  function channelNames(r: OncallRotation): string {
    return (r.channels ?? []).map((id) => channelList.find((c) => c.id === id)?.name ?? id.slice(0, 8)).join(' → ');
  }

  async function onSave(body: OncallRotationInput) {
    if (editing && editing !== 'new') {
      await update.mutateAsync({ ...body, id: editing.id, revision: editing.revision });
    } else {
      await create.mutateAsync(body);
    }
  }

  async function onDelete(r: OncallRotation) {
    if (!(await ask({ message: r8.deleteConfirm(r.name), confirmLabel: t.common.delete, destructive: true }))) return;
    try {
      await remove.mutateAsync(r.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  return (
    <>
      {header}
      <div className="flex items-center justify-between">
        <span className="text-[13px] text-muted-foreground">{r8.subtitle}</span>
        {canWrite && (
          <Button variant="primary" onClick={() => setEditing('new')}>
            <CalendarClock size={15} /> {r8.newRotation}
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{r8.columns.rotation}</span>
          <span>{r8.columns.period}</span>
          <span>{r8.columns.cycle}</span>
          <span />
        </GridHeader>
        {list.map((r) => (
          <GridRow key={r.id} columns={COLUMNS}>
            <span className="truncate text-[13px] font-medium" data-row>
              {r.name}
            </span>
            <span className="text-xs text-muted-foreground">{r8.periodDays(r.period_days)}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{channelNames(r)}</span>
            <span className="flex justify-end gap-1">
              {canWrite && (
                <>
                  <Button type="button" variant="ghost" aria-label={m.editAria(r.name)} onClick={() => setEditing(r)}>
                    {m.edit}
                  </Button>
                  <Button type="button" variant="destructive" aria-label={m.deleteAria(r.name)} onClick={() => void onDelete(r)}>
                    {t.common.delete}
                  </Button>
                </>
              )}
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{r8.empty}</p>}
      </Card>
      {list.map((r) => (
        <StandIns key={r.id} org={org} rotation={r} channels={channelOptions} mailAvailable={!!channels.data?.mail_available} canWrite={canWrite} />
      ))}
      {editing && (
        <RotationDialog
          open
          rotation={editing === 'new' ? undefined : editing}
          channels={channelOptions}
          onClose={() => setEditing(null)}
          onSave={onSave}
        />
      )}
    </>
  );
}

interface StandInsChannel {
  id: string;
  name: string;
}

/**
 * StandIns is one rotation's roster-and-exceptions section: RosterStrip
 * for the next two weeks, the exceptions already on file, and (for
 * writers) a small form to add one. An exception is create/delete only —
 * oncall.ValidateException's own shape (OncallExceptionBody's doc
 * comment: no update, no revision) — so there is nothing here to edit,
 * only to add or remove.
 */
function StandIns({
  org, rotation, channels, mailAvailable, canWrite,
}: { org: string; rotation: OncallRotation; channels: StandInsChannel[]; mailAvailable: boolean; canWrite: boolean }) {
  const t = useT();
  const fmt = useFormat();
  const m = t.notifications;
  const r8 = m.rotations;
  const toast = useToast();
  const [range] = useState(rosterRange);
  const shifts = useOncallShifts(org, rotation.id, range.from, range.to);
  const exceptions = useOncallExceptions(org, rotation.id);
  const createException = useCreateOncallException(org, rotation.id);
  const deleteException = useDeleteOncallException(org, rotation.id);
  const [adding, setAdding] = useState(false);
  const [channelId, setChannelId] = useState(channels[0]?.id ?? '');
  const [startsAt, setStartsAt] = useState(toLocalInput(new Date().toISOString()));
  const [endsAt, setEndsAt] = useState(toLocalInput(new Date(new Date().getTime() + 86_400_000).toISOString()));
  const [note, setNote] = useState('');
  // Non-null while the "New channel…" row's dialog is open; its value is
  // whatever had been typed into the picker, so ChannelDialog can prefill
  // the Name field with it (null, not '', is "closed") — same pattern as
  // GroupSettingsDialog's own newBastionName (v0.4g Task 5).
  const [newChannelName, setNewChannelName] = useState<string | null>(null);

  async function onAdd() {
    try {
      await createException.mutateAsync({
        channel_id: channelId,
        starts_at: fromLocalInput(startsAt),
        ends_at: fromLocalInput(endsAt),
        ...(note.trim() !== '' ? { note: note.trim() } : {}),
      });
      setAdding(false);
      setNote('');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : r8.addFailed, 'destructive');
    }
  }

  async function onRemove(exceptionId: string) {
    try {
      await deleteException.mutateAsync(exceptionId);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : r8.removeFailed, 'destructive');
    }
  }

  const exceptionList = exceptions.data?.exceptions ?? [];

  return (
    // The create-row dialog renders outside this <section>, as its own
    // sibling, not nested inside: a submit inside it is a native "submit"
    // event, and ui/dialog.tsx does not portal, so nesting it inside any
    // ancestor <form> would let that submit bubble into the wrong handler
    // (the same reasoning GroupSettingsDialog.tsx already documents for
    // BastionDialog). StandIns' own "Add" button isn't a form submit, so
    // this isn't that bug today — but keeping the same shape as every
    // other quick-create call site means it stays correct if that ever
    // changes.
    <>
      <section aria-label={r8.rosterAria(rotation.name)} className="flex flex-col gap-2 rounded-xl border border-border bg-card p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-semibold">{r8.rosterTitle(rotation.name)}</h2>
          {canWrite && (
            <Button type="button" variant="ghost" onClick={() => setAdding(!adding)}>
              <Plus size={13} /> {r8.addStandIn}
            </Button>
          )}
        </div>
        {/* A roster that cannot be built arrives here as a FAILED REQUEST,
            never as an empty shift list: oncall.Shifts always tiles the
            range it is given, so RosterStrip's own empty branch is
            unreachable from this page. The cases the spec's "Error
            handling" section calls ordinary — the rotation's last channel
            deleted, a timezone whose zoneinfo no longer loads — all land in
            this branch, and before it existed the section printed
            "Loading roster…" forever (whole-branch review, Important #4).
            It is said in the same words a channel-less route is said in
            (ReachesNobody, AlertRouteCells.tsx), because it is the same
            fact: alerts routed here reach nobody. */}
        {shifts.isError ? (
          <p role="alert" className="flex items-center gap-1.5 text-xs font-semibold text-destructive">
            <BellOff size={13} aria-hidden="true" />
            {r8.rosterFailed}
            {shifts.error instanceof Error ? ` (${shifts.error.message})` : ''}
          </p>
        ) : shifts.data ? (
          <RosterStrip shifts={shifts.data.shifts ?? []} now={new Date().toISOString()} />
        ) : (
          <p className="text-xs text-muted-foreground">{r8.loadingRoster}</p>
        )}
        {adding && (
          <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1.4fr)_80px] items-end gap-2 rounded-md border border-border p-2.5">
            <div className="flex flex-col gap-1">
              <Label htmlFor={`exc-channel-${rotation.id}`}>{r8.channel}</Label>
              <Select
                id={`exc-channel-${rotation.id}`}
                options={channels.map((c) => ({ value: c.id, label: c.name }))}
                value={channelId}
                onChange={setChannelId}
                create={{ label: r8.newChannel, onSelect: setNewChannelName }}
              />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={`exc-start-${rotation.id}`}>{m.from}</Label>
              <DateTimePicker id={`exc-start-${rotation.id}`} value={startsAt} onChange={setStartsAt} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={`exc-end-${rotation.id}`}>{m.until}</Label>
              <DateTimePicker id={`exc-end-${rotation.id}`} value={endsAt} onChange={setEndsAt} />
            </div>
            <div className="flex flex-col gap-1">
              <Label htmlFor={`exc-note-${rotation.id}`}>{m.note}</Label>
              <Input id={`exc-note-${rotation.id}`} value={note} onChange={(e) => setNote(e.target.value)} maxLength={1000} />
            </div>
            {/* No <form> here to block an empty field, so Add itself waits
                for both instants: fromLocalInput('') would throw. */}
            <Button
              type="button"
              variant="primary"
              onClick={() => void onAdd()}
              disabled={!channelId || startsAt === '' || endsAt === '' || createException.isPending}
            >
              {r8.add}
            </Button>
          </div>
        )}
        {exceptionList.length > 0 && (
          <ul className="flex flex-col gap-1">
            {exceptionList.map((e) => (
              <li key={e.id} className="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                <span className="truncate">
                  {r8.standsIn(channels.find((c) => c.id === e.channel_id)?.name ?? e.channel_id.slice(0, 8), fmt.dateTime(e.starts_at), fmt.dateTime(e.ends_at))}
                  {e.note ? ` — ${e.note}` : ''}
                </span>
                {canWrite && (
                  <Button type="button" variant="ghost" aria-label={r8.removeStandInAria(channels.find((c) => c.id === e.channel_id)?.name ?? e.channel_id.slice(0, 8))} onClick={() => void onRemove(e.id)}>
                    <Trash2 size={13} />
                  </Button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
      {newChannelName !== null && (
        <ChannelDialog
          org={org}
          mailAvailable={mailAvailable}
          initialName={newChannelName}
          onClose={() => setNewChannelName(null)}
          onCreated={(id) => {
            setChannelId(id);
            setNewChannelName(null);
          }}
        />
      )}
    </>
  );
}
