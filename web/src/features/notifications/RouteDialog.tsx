import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import type { AlertRoute, AlertRouteEventKind, AlertRouteInput, AlertRouteScope, AlertRouteSeverity } from '../../api/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

/** A route's channel, trimmed to what the picker needs — never the full
 * NotifyChannel DTO, so a caller does not have to thread webhook secrets
 * or delivery settings through just to let a route pick a channel. */
export interface RouteDialogChannel {
  id: string;
  name: string;
}

/** A route's scope target (a host or a monitoring group), trimmed the same
 * way RouteDialogChannel is. */
export interface RouteDialogTarget {
  id: string;
  name: string;
}

const SEVERITIES: { value: AlertRouteSeverity; label: string }[] = [
  { value: 'warn', label: 'Warning' },
  { value: 'crit', label: 'Critical' },
  { value: 'unknown', label: 'Unknown' },
];

/** The seven event kinds (internal/alerts.EventKind), grouped into two
 * labelled fieldsets rather than one wall of seven checkboxes: State is
 * what the alert itself did (opened, worsened — the wire's
 * severity_raised — or closed); Operator action is what a person did
 * about it. The wire values differ from these labels on purpose: "severity_raised"
 * reads as "worsened" here, matching how the Alerts page already narrates
 * the same event (alertTimeline.ts). */
const STATE_EVENTS: { value: AlertRouteEventKind; label: string }[] = [
  { value: 'opened', label: 'Opened' },
  { value: 'severity_raised', label: 'Worsened' },
  { value: 'closed', label: 'Closed' },
];
const OPERATOR_EVENTS: { value: AlertRouteEventKind; label: string }[] = [
  { value: 'acknowledged', label: 'Acknowledged' },
  { value: 'ack_voided', label: 'Acknowledgement voided' },
  { value: 'snoozed', label: 'Snoozed' },
  { value: 'unsnoozed', label: 'Unsnoozed' },
];

function toggle<T>(list: T[], value: T): T[] {
  return list.includes(value) ? list.filter((v) => v !== value) : [...list, value];
}

export interface RouteDialogProps {
  open: boolean;
  /** The route to edit; undefined creates one. */
  route?: AlertRoute;
  channels: RouteDialogChannel[];
  hosts?: RouteDialogTarget[];
  groups?: RouteDialogTarget[];
  onClose: () => void;
  /** Saving is the caller's job (AlertRoutesPage holds the create/update
   * mutations, since those need the organization and, on update, the
   * route's own revision for If-Match) — this dialog only ever decides
   * whether the request is complete enough to send at all. */
  onSave?: (body: AlertRouteInput) => void | Promise<void>;
}

/** RouteDialog creates or edits one alert route: its name, scope, which
 * severities and lifecycle events it fires on, and which channels it
 * delivers to. Save stays disabled until all four are present — the same
 * four rules alertroutes.Validate enforces server-side (a route needs a
 * name, at least one severity, at least one event kind and at least one
 * channel) — so this dialog never offers a request the API would refuse
 * with a route that then sits in the list looking configured while
 * telling nobody (backlog 4.29). */
export function RouteDialog({ open, route, channels, hosts = [], groups = [], onClose, onSave }: RouteDialogProps) {
  const editing = !!route;
  const [name, setName] = useState(route?.name ?? '');
  const [enabled, setEnabled] = useState(route?.enabled ?? true);
  const [scope, setScope] = useState<AlertRouteScope>(route?.scope ?? 'org');
  const [scopeId, setScopeId] = useState(route?.scope_id ?? '');
  const [severities, setSeverities] = useState<AlertRouteSeverity[]>((route?.severities ?? []) as AlertRouteSeverity[]);
  const [events, setEvents] = useState<AlertRouteEventKind[]>((route?.events ?? []) as AlertRouteEventKind[]);
  const [selectedChannels, setSelectedChannels] = useState<string[]>(route?.channels ?? []);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const valid = name.trim() !== '' && severities.length > 0 && events.length > 0 && selectedChannels.length > 0;

  function body(): AlertRouteInput {
    return {
      name,
      enabled,
      scope,
      ...(scope !== 'org' && scopeId ? { scope_id: scopeId } : {}),
      severities,
      events,
      channels: selectedChannels,
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
      setError(err instanceof Error ? err.message : 'Saving failed');
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={editing ? `Edit ${route.name}` : 'New route'} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="rt-name">Name</Label>
        <Input id="rt-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Enabled
        </label>
        <Label htmlFor="rt-scope">Applies to</Label>
        <select
          id="rt-scope"
          className={SELECT}
          value={scope}
          onChange={(e) => {
            setScope(e.target.value as AlertRouteScope);
            setScopeId('');
          }}
        >
          <option value="org">The whole organization</option>
          <option value="monitoring_group">A monitoring group</option>
          <option value="host">One host</option>
        </select>
        {scope === 'monitoring_group' && (
          <select aria-label="Monitoring group" className={SELECT} value={scopeId} onChange={(e) => setScopeId(e.target.value)} required>
            <option value="">Choose a group…</option>
            {groups.map((g) => (
              <option key={g.id} value={g.id}>
                {g.name}
              </option>
            ))}
          </select>
        )}
        {scope === 'host' && (
          <select aria-label="Host" className={SELECT} value={scopeId} onChange={(e) => setScopeId(e.target.value)} required>
            <option value="">Choose a host…</option>
            {hosts.map((h) => (
              <option key={h.id} value={h.id}>
                {h.name}
              </option>
            ))}
          </select>
        )}
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">Severity</legend>
          <div className="flex flex-wrap gap-3">
            {SEVERITIES.map((s) => (
              <label key={s.value} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={severities.includes(s.value)} onChange={() => setSeverities(toggle(severities, s.value))} />
                {s.label}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">State</legend>
          <div className="flex flex-wrap gap-3">
            {STATE_EVENTS.map((ev) => (
              <label key={ev.value} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={events.includes(ev.value)} onChange={() => setEvents(toggle(events, ev.value))} />
                {ev.label}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">Operator action</legend>
          <div className="flex flex-wrap gap-3">
            {OPERATOR_EVENTS.map((ev) => (
              <label key={ev.value} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={events.includes(ev.value)} onChange={() => setEvents(toggle(events, ev.value))} />
                {ev.label}
              </label>
            ))}
          </div>
        </fieldset>
        <span className="text-[11px] font-medium text-muted-foreground">Channels</span>
        <div className="flex flex-wrap gap-3">
          {channels.map((c) => (
            <label key={c.id} className="flex items-center gap-1.5 text-xs">
              <input type="checkbox" checked={selectedChannels.includes(c.id)} onChange={() => setSelectedChannels(toggle(selectedChannels, c.id))} />
              {c.name}
            </label>
          ))}
          {channels.length === 0 && <span className="text-xs text-muted-foreground">Add a notification channel first.</span>}
        </div>
        {error && (
          <p role="alert" className="text-xs text-[var(--px-crit)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!valid || pending}>
            Save
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
