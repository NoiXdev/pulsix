import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { TimeField } from '../../ui/TimeField.tsx';
import { Label } from '../../ui/label.tsx';
import { Switch } from '../../ui/switch.tsx';
import { Select } from '../../ui/Select.tsx';
import type { AlertRoute, AlertRouteEventKind, AlertRouteInput, AlertRouteScope, AlertRouteSeverity, AlertRouteStep } from '../../api/types.ts';
import { WEEKDAYS, compileWindow, decompileWindow, severityLabel, type Weekday, type WindowDraft } from './alertRoute.ts';
import { StepsEditor } from './StepsEditor.tsx';
import { stepsAreComplete } from './steps.ts';
import { useT } from '../../i18n/useT.ts';

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

const SEVERITIES: AlertRouteSeverity[] = ['warn', 'crit', 'unknown'];

/** The seven event kinds (internal/alerts.EventKind), grouped into two
 * labelled fieldsets rather than one wall of seven checkboxes: State is
 * what the alert itself did (opened, worsened — the wire's
 * severity_raised — or closed); Operator action is what a person did
 * about it. The wire values differ from these labels on purpose: "severity_raised"
 * reads as "worsened" here, matching how the Alerts page already narrates
 * the same event (alertTimeline.ts). */
const STATE_EVENTS = ['opened', 'severity_raised', 'closed'] as const satisfies readonly AlertRouteEventKind[];
const OPERATOR_EVENTS = ['acknowledged', 'ack_voided', 'snoozed', 'unsnoozed'] as const satisfies readonly AlertRouteEventKind[];

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
  const t = useT();
  const m = t.notifications;
  const d = m.routeDialog;
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
  const [steps, setSteps] = useState<AlertRouteStep[]>(route?.steps ?? []);
  const [pending, setPending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Breakthrough offers only the severities this route already fires on
  // (the server rejects any other one) and only while a window narrows
  // delivery at all — without a window there is nothing to cross.
  const breakthroughOptions = windowEnabled ? SEVERITIES.filter((s) => severities.includes(s)) : [];

  const channelsValid = channelSource === 'fixed' ? selectedChannels.length > 0 : rotationId !== '';
  const windowValid = !windowEnabled || windowDraft.days.length > 0;
  // stepsAreComplete mirrors channelsValid's own either/or rule, applied
  // per rung (StepsEditor's own doc comment on why that is not a second
  // copy of alertroutes.ValidateSteps): an incomplete rung — "fixed"
  // picked with no channel ticked yet, or "rotation" picked with none
  // chosen — is a 400 the server would refuse, the same reason an
  // incomplete route-level channel source already gates Save below.
  const stepsValid = stepsAreComplete(steps);
  const valid = name.trim() !== '' && severities.length > 0 && events.length > 0 && channelsValid && windowValid && stepsValid;

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
      ...(steps.length > 0 ? { steps } : {}),
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
      setError(err instanceof Error ? err.message : m.savingFailed);
    } finally {
      setPending(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={editing ? d.editTitle(route.name) : d.newTitle} size="wide">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="rt-name">{m.name}</Label>
        <Input id="rt-name" value={name} onChange={(e) => setName(e.target.value)} required maxLength={100} />
        <div className="flex items-center gap-2 text-xs">
          <Switch id="rt-enabled" checked={enabled} onCheckedChange={setEnabled} />
          <label htmlFor="rt-enabled">{m.enabledLabel}</label>
        </div>
        <Label htmlFor="rt-scope">{d.appliesTo}</Label>
        <Select
          id="rt-scope"
          options={[
            { value: 'org', label: d.scopes.org },
            { value: 'monitoring_group', label: d.scopes.monitoring_group },
            { value: 'host', label: d.scopes.host },
          ]}
          value={scope}
          onChange={(v) => {
            setScope(v as AlertRouteScope);
            setScopeId('');
          }}
        />
        {scope === 'monitoring_group' && (
          <Select
            aria-label={d.monitoringGroup}
            options={groups.map((g) => ({ value: g.id, label: g.name }))}
            value={scopeId}
            onChange={setScopeId}
            placeholder={d.chooseGroup}
            required
          />
        )}
        {scope === 'host' && (
          <Select
            aria-label={d.host}
            options={hosts.map((h) => ({ value: h.id, label: h.name }))}
            value={scopeId}
            onChange={setScopeId}
            placeholder={d.chooseHost}
            required
          />
        )}
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.severity}</legend>
          <div className="flex flex-wrap gap-3">
            {SEVERITIES.map((s) => (
              <label key={s} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={severities.includes(s)} onChange={() => setSeverities(toggle(severities, s))} />
                {severityLabel(s, t)}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.state}</legend>
          <div className="flex flex-wrap gap-3">
            {STATE_EVENTS.map((ev) => (
              <label key={ev} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={events.includes(ev)} onChange={() => setEvents(toggle(events, ev))} />
                {d.stateEvents[ev]}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.operatorAction}</legend>
          <div className="flex flex-wrap gap-3">
            {OPERATOR_EVENTS.map((ev) => (
              <label key={ev} className="flex items-center gap-1.5 text-xs">
                <input type="checkbox" checked={events.includes(ev)} onChange={() => setEvents(toggle(events, ev))} />
                {d.operatorEvents[ev]}
              </label>
            ))}
          </div>
        </fieldset>
        <fieldset className="flex flex-col gap-1.5 rounded-md border border-border p-2.5">
          <legend className="px-1 text-[11px] font-medium text-muted-foreground">{m.channelSource}</legend>
          <div role="radiogroup" aria-label={m.channelSource} className="flex gap-3">
            <label className="flex items-center gap-1.5 text-xs">
              <input type="radio" name="rt-channel-source" checked={channelSource === 'fixed'} onChange={() => setChannelSource('fixed')} />
              {m.fixedList}
            </label>
            <label className="flex items-center gap-1.5 text-xs">
              <input type="radio" name="rt-channel-source" checked={channelSource === 'rotation'} onChange={() => setChannelSource('rotation')} />
              {m.onCallRotation}
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
              {channels.length === 0 && <span className="text-xs text-muted-foreground">{m.addChannelFirst}</span>}
            </div>
          ) : (
            <>
              <Select
                aria-label={m.rotation}
                options={rotations.map((r) => ({ value: r.id, label: r.name }))}
                value={rotationId}
                onChange={setRotationId}
                placeholder={m.chooseRotation}
                required
              />
              {rotations.length === 0 && <span className="text-xs text-muted-foreground">{m.setUpRotationFirst}</span>}
            </>
          )}
        </fieldset>
        <StepsEditor steps={steps} onChange={setSteps} channels={channels} rotations={rotations} />
        <div className="flex items-center gap-2 text-xs">
          <Switch id="rt-window-enabled" checked={windowEnabled} onCheckedChange={setWindowEnabled} />
          <label htmlFor="rt-window-enabled">{d.windowOnly}</label>
        </div>
        {windowEnabled && (
          <fieldset className="flex flex-col gap-2 rounded-md border border-border p-2.5">
            <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.deliveryWindow}</legend>
            <div className="flex flex-wrap gap-3">
              {WEEKDAYS.map((day) => (
                <label key={day} className="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={windowDraft.days.includes(day)} onChange={() => toggleWeekday(day)} />
                  {m.weekdayShort[day]}
                </label>
              ))}
            </div>
            <div className="grid grid-cols-3 gap-2">
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-from">{m.from}</Label>
                <TimeField id="rt-window-from" value={windowDraft.from} onChange={(v) => setWindowDraft({ ...windowDraft, from: v })} required />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-until">{m.until}</Label>
                <TimeField id="rt-window-until" value={windowDraft.until} onChange={(v) => setWindowDraft({ ...windowDraft, until: v })} required />
              </div>
              <div className="flex flex-col gap-1">
                <Label htmlFor="rt-window-tz">{m.timezone}</Label>
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
            <legend className="px-1 text-[11px] font-medium text-muted-foreground">{d.breakthrough}</legend>
            <div className="flex flex-wrap gap-3">
              {breakthroughOptions.map((s) => (
                <label key={s} className="flex items-center gap-1.5 text-xs">
                  <input type="checkbox" checked={breakthrough.includes(s)} onChange={() => setBreakthrough(toggle(breakthrough, s))} />
                  {severityLabel(s, t)}
                </label>
              ))}
              {breakthroughOptions.length === 0 && <span className="text-xs text-muted-foreground">{d.pickSeverity}</span>}
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
