import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import type { AlertRoute, AlertRouteEventKind, AlertRouteInput, AlertRouteScope, AlertRouteSeverity } from '../../api/types.ts';
import { WEEKDAY_OPTIONS, compileWindow, decompileWindow, type Weekday, type WindowDraft } from './alertRoute.ts';

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

/** An on-call rotation the combobox can offer as a route's channel
 * source, trimmed the same way RouteDialogChannel is — the dialog never
 * needs a rotation's roster or exceptions, only enough to list it. */
export interface RouteDialogRotation {
  id: string;
  name: string;
}

type ChannelSource = 'fixed' | 'rotation';

const DEFAULT_WINDOW_DRAFT: WindowDraft = { days: [], from: '09:00', until: '17:00', timezone: Intl.DateTimeFormat().resolvedOptions().timeZone };

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
  rotations?: RouteDialogRotation[];
  onClose: () => void;
  /** Saving is the caller's job (AlertRoutesPage holds the create/update
   * mutations, since those need the organization and, on update, the
   * route's own revision for If-Match) — this dialog only ever decides
   * whether the request is complete enough to send at all. */
  onSave?: (body: AlertRouteInput) => void | Promise<void>;
}

/** RouteDialog creates or edits one alert route: its name, scope, which
 * severities and lifecycle events it fires on, which channels it delivers
 * to (a fixed list or an on-call rotation), and — v0.4c's own addition —
 * an optional delivery window with the severities allowed to break
 * through it. Save stays disabled until a route needs a name, at least
 * one severity, at least one event kind, a channel source that actually
 * names something, and (when a window is on) at least one weekday — the
 * same shape alertroutes.Validate enforces server-side — so this dialog
 * never offers a request the API would refuse with a route that then sits
 * in the list looking configured while telling nobody (backlog 4.29).
 * Breakthrough severities are restricted to this route's own severities
 * and only shown once a window exists, for the same reason: the server
 * rejects a breakthrough severity outside that set, and without a window
 * there is nothing for one to cross. */
export function RouteDialog({ open, route, channels, hosts = [], groups = [], rotations = [], onClose, onSave }: RouteDialogProps) {
  const editing = !!route;
  const [name, setName] = useState(route?.name ?? '');
  const [enabled, setEnabled] = useState(route?.enabled ?? true);
  const [scope, setScope] = useState<AlertRouteScope>(route?.scope ?? 'org');
  const [scopeId, setScopeId] = useState(route?.scope_id ?? '');
  const [severities, setSeverities] = useState<AlertRouteSeverity[]>((route?.severities ?? []) as AlertRouteSeverity[]);
  const [events, setEvents] = useState<AlertRouteEventKind[]>((route?.events ?? []) as AlertRouteEventKind[]);
  const [selectedChannels, setSelectedChannels] = useState<string[]>(route?.channels ?? []);
  const [channelSource, setChannelSource] = useState<ChannelSource>(route?.rotation_id ? 'rotation' : 'fixed');
  const [rotationId, setRotationId] = useState(route?.rotation_id ?? '');
  const [windowEnabled, setWindowEnabled] = useState(!!route?.window);
  const [windowDraft, setWindowDraft] = useState<WindowDraft>(route?.window ? decompileWindow(route.window) : DEFAULT_WINDOW_DRAFT);
  const [breakthrough, setBreakthrough] = useState<AlertRouteSeverity[]>((route?.breakthrough_severities ?? []) as AlertRouteSeverity[]);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Breakthrough offers only the severities this route already fires on
  // (the server rejects any other one) and only while a window narrows
  // delivery at all — without a window there is nothing to cross.
  const breakthroughOptions = windowEnabled ? SEVERITIES.filter((s) => severities.includes(s.value)) : [];

  const channelsValid = channelSource === 'fixed' ? selectedChannels.length > 0 : rotationId !== '';
  const windowValid = !windowEnabled || windowDraft.days.length > 0;
  const valid = name.trim() !== '' && severities.length > 0 && events.length > 0 && channelsValid && windowValid;

  function toggleWeekday(day: Weekday) {
    setWindowDraft({ ...windowDraft, days: toggle(windowDraft.days, day) });
  }

  function body(): AlertRouteInput {
    return {
      name,
      enabled,
      scope,
      ...(scope !== 'org' && scopeId ? { scope_id: scopeId } : {}),
      severities,
      events,
      ...(channelSource === 'rotation' ? { rotation_id: rotationId } : { channels: selectedChannels }),
      ...(windowEnabled ? { window: compileWindow(windowDraft) } : {}),
      ...(windowEnabled && breakthrough.length > 0 ? { breakthrough_severities: breakthrough.filter((s) => severities.includes(s)) } : {}),
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
        <Select
          id="rt-scope"
          options={[
            { value: 'org', label: 'The whole organization' },
            { value: 'monitoring_group', label: 'A monitoring group' },
            { value: 'host', label: 'One host' },
          ]}
          value={scope}
          onChange={(v) => {
            setScope(v as AlertRouteScope);
            setScopeId('');
          }}
        />
        {scope === 'monitoring_group' && (
          <Select
            aria-label="Monitoring group"
            options={groups.map((g) => ({ value: g.id, label: g.name }))}
            value={scopeId}
            onChange={setScopeId}
            placeholder="Choose a group…"
            required
          />
        )}
        {scope === 'host' && (
          <Select
            aria-label="Host"
            options={hosts.map((h) => ({ value: h.id, label: h.name }))}
            value={scopeId}
            onChange={setScopeId}
            placeholder="Choose a host…"
            required
          />
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
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">Channel source</legend>
          <div role="radiogroup" aria-label="Channel source" className="flex gap-3">
            <label className="flex items-center gap-1.5 text-xs">
              <input type="radio" name="rt-channel-source" checked={channelSource === 'fixed'} onChange={() => setChannelSource('fixed')} />
              Fixed list
            </label>
            <label className="flex items-center gap-1.5 text-xs">
              <input type="radio" name="rt-channel-source" checked={channelSource === 'rotation'} onChange={() => setChannelSource('rotation')} />
              On-call rotation
            </label>
          </div>
          {channelSource === 'fixed' ? (
            <div className="flex flex-wrap gap-3">
              {channels.map((c) => (
                <label key={c.id} className="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={selectedChannels.includes(c.id)} onChange={() => setSelectedChannels(toggle(selectedChannels, c.id))} />
                  {c.name}
                </label>
              ))}
              {channels.length === 0 && <span className="text-xs text-muted-foreground">Add a notification channel first.</span>}
            </div>
          ) : (
            <>
              <Select
                aria-label="Rotation"
                options={rotations.map((r) => ({ value: r.id, label: r.name }))}
                value={rotationId}
                onChange={setRotationId}
                placeholder="Choose a rotation…"
                required
              />
              {rotations.length === 0 && <span className="text-xs text-muted-foreground">Set up an on-call rotation first.</span>}
            </>
          )}
        </fieldset>
        <label className="flex items-center gap-2 text-xs">
          <input type="checkbox" checked={windowEnabled} onChange={(e) => setWindowEnabled(e.target.checked)} />
          Only deliver during a window
        </label>
        {windowEnabled && (
          <fieldset className="flex flex-col gap-2 rounded-md border border-border p-2.5">
            <legend className="px-1 text-[11px] font-medium text-muted-foreground">Delivery window</legend>
            <div className="flex flex-wrap gap-3">
              {WEEKDAY_OPTIONS.map((d) => (
                <label key={d.value} className="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={windowDraft.days.includes(d.value)} onChange={() => toggleWeekday(d.value)} />
                  {d.label}
                </label>
              ))}
            </div>
            <div className="grid grid-cols-3 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-from">From</Label>
                <Input
                  id="rt-window-from"
                  type="time"
                  value={windowDraft.from}
                  onChange={(e) => setWindowDraft({ ...windowDraft, from: e.target.value })}
                  required
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-until">Until</Label>
                <Input
                  id="rt-window-until"
                  type="time"
                  value={windowDraft.until}
                  onChange={(e) => setWindowDraft({ ...windowDraft, until: e.target.value })}
                  required
                />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-tz">Timezone</Label>
                <Input
                  id="rt-window-tz"
                  className="font-mono"
                  value={windowDraft.timezone}
                  onChange={(e) => setWindowDraft({ ...windowDraft, timezone: e.target.value })}
                />
              </div>
            </div>
          </fieldset>
        )}
        {windowEnabled && (
          <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
            <legend className="px-1 text-[11px] font-medium text-muted-foreground">Also let these severities through outside the window too</legend>
            <div className="flex flex-wrap gap-3">
              {breakthroughOptions.map((s) => (
                <label key={s.value} className="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={breakthrough.includes(s.value)} onChange={() => setBreakthrough(toggle(breakthrough, s.value))} />
                  {s.label}
                </label>
              ))}
              {breakthroughOptions.length === 0 && <span className="text-xs text-muted-foreground">Pick a severity above to offer it here.</span>}
            </div>
          </fieldset>
        )}
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
