import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { ApiError } from '../../api/client.ts';
import {
  type SchedulePreviewBody,
  useCreateSchedule,
  useDeleteSchedule,
  useRunScheduleNow,
  useSchedule,
  useScheduleAction,
  useSchedulePreview,
  useUpdateSchedule,
} from '../../api/hooks/useSchedules.ts';
import type { Schedule, ScheduleInput, Template } from '../../api/types.ts';
import { HOUR_STEPS, MINUTE_STEPS, WEEKDAYS, fromSimpleBody, toSimpleBody, type SimpleForm, type SimpleKind } from './simple.ts';
import { ScheduleNotifications } from './ScheduleNotifications.tsx';
import { ScheduleRuns } from './ScheduleRuns.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

const WINDOWS = ['15m', '30m', '1h', '2h', '6h', '12h', '24h'];
type MaintenanceGuard = 'ignore' | 'skip' | 'exclude_hosts';
const GUARDS: MaintenanceGuard[] = ['ignore', 'skip', 'exclude_hosts'];
const KINDS: SimpleKind[] = ['every_minutes', 'every_hours', 'daily', 'weekly', 'monthly'];

export interface ScheduleEditorProps {
  org: string;
  /** The schedule id, or "new". */
  id: string;
  templates: Template[];
  defaultTemplate?: string;
  canWrite: boolean;
  canRun: boolean;
  onClose: () => void;
}

/** ScheduleEditor is the Scheduler artboard's detail panel: template,
 * timezone, simple/cron switch with the next five occurrences, guards,
 * overrides, the recent runs and the run now / pause / save actions. */
export function ScheduleEditor(props: ScheduleEditorProps) {
  const aria = useT().schedules.editor.aria;
  const isNew = props.id === 'new';
  const detail = useSchedule(props.org, isNew ? '' : props.id);
  const initial = isNew ? undefined : detail.data?.schedule;
  return (
    <section aria-label={aria} className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
      {!isNew && !initial ? <QueryLoading /> : <EditorForm key={initial ? `${initial.id}-${initial.revision}` : 'new'} {...props} initial={initial} />}
    </section>
  );
}

function EditorForm({ org, templates, defaultTemplate, canWrite, canRun, onClose, initial }: ScheduleEditorProps & { initial?: Schedule }) {
  const t = useT();
  const fmt = useFormat();
  const m = t.schedules.editor;
  const navigate = useNavigate();
  const toast = useToast();
  const create = useCreateSchedule(org);
  const update = useUpdateSchedule(org, initial?.id ?? '');
  const remove = useDeleteSchedule(org);
  const pause = useScheduleAction(org, 'pause');
  const resume = useScheduleAction(org, 'resume');
  const runNow = useRunScheduleNow(org);

  const [name, setName] = useState(initial?.name ?? '');
  const [templateId, setTemplateId] = useState(initial?.template_id ?? defaultTemplate ?? templates[0]?.id ?? '');
  const [timezone, setTimezone] = useState(initial?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone);
  const [mode, setMode] = useState<'cron' | 'simple'>(initial ? (initial.mode as 'cron' | 'simple') : 'simple');
  const [cron, setCron] = useState(initial?.cron ?? '0 3 * * *');
  const [simple, setSimple] = useState<SimpleForm>(fromSimpleBody(initial?.simple));
  const [overlapSkip, setOverlapSkip] = useState((initial?.guards.overlap ?? 'skip') === 'skip');
  const [catchUp, setCatchUp] = useState(initial?.guards.catch_up?.enabled ?? false);
  const [catchUpWindow, setCatchUpWindow] = useState(initial?.guards.catch_up?.window || '1h');
  const [maintenance, setMaintenance] = useState<MaintenanceGuard>((initial?.guards.maintenance as MaintenanceGuard) ?? 'ignore');
  const [limit, setLimit] = useState(initial?.limit ?? '');
  const [extraVars, setExtraVars] = useState(initial?.extra_vars ? JSON.stringify(initial.extra_vars, null, 2) : '');
  const [error, setError] = useState<string | null>(null);

  const previewBody = useMemo<SchedulePreviewBody>(
    () => (mode === 'simple' ? { mode, simple: toSimpleBody(simple), timezone } : { mode, cron, timezone }),
    [mode, simple, cron, timezone],
  );
  const [debounced, setDebounced] = useState<SchedulePreviewBody | null>(previewBody);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(previewBody), 300);
    return () => clearTimeout(t);
  }, [previewBody]);
  const preview = useSchedulePreview(org, debounced);

  function setKind(kind: SimpleKind) {
    setSimple({ ...simple, kind, every: kind === 'every_hours' ? 1 : kind === 'every_minutes' ? 15 : simple.every });
  }

  function toggleDay(day: string) {
    setSimple({ ...simple, days: simple.days.includes(day) ? simple.days.filter((d) => d !== day) : [...simple.days, day] });
  }

  function body(): ScheduleInput | null {
    let vars: Record<string, unknown> | undefined;
    if (extraVars.trim() !== '') {
      try {
        vars = JSON.parse(extraVars) as Record<string, unknown>;
      } catch {
        setError(m.extraVarsInvalid);
        return null;
      }
    }
    return {
      template_id: templateId,
      name,
      mode,
      ...(mode === 'simple' ? { simple: toSimpleBody(simple) } : { cron }),
      timezone,
      guards: { overlap: overlapSkip ? 'skip' : 'queue', catch_up: { enabled: catchUp, window: catchUpWindow }, maintenance },
      ...(limit !== '' ? { limit } : {}),
      ...(vars ? { extra_vars: vars } : {}),
    } as ScheduleInput;
  }

  async function save() {
    setError(null);
    const b = body();
    if (!b) return;
    try {
      if (initial) {
        await update.mutateAsync({ ...b, revision: initial.revision });
      } else {
        const res = await create.mutateAsync(b);
        void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: res.schedule.id, template: undefined } });
      }
      toast.show(m.saved);
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        setError(m.changedSinceLoad);
      } else {
        setError(err instanceof Error ? err.message : m.saveFailed);
      }
    }
  }

  async function run() {
    if (!initial) return;
    try {
      const res = await runNow.mutateAsync(initial.id);
      const first = res.jobs?.[0];
      if (first) await navigate({ to: '/o/$org/runs/$id', params: { org, id: first.id } });
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.runFailed, 'destructive');
    }
  }

  async function del() {
    if (!initial || !confirm(m.deleteConfirm(initial.name))) return;
    try {
      await remove.mutateAsync(initial.id);
      onClose();
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.deleteFailed, 'destructive');
    }
  }

  async function toggle() {
    if (!initial) return;
    try {
      await (initial.paused ? resume : pause).mutateAsync(initial.id);
    } catch (err) {
      toast.show(err instanceof Error ? err.message : (initial.paused ? m.resumeFailed : m.pauseFailed), 'destructive');
    }
  }

  const zoneFmt = (at: string) => fmt.dateTime(at, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short' });
  const previewError = preview.error instanceof Error ? preview.error.message : null;

  return (
    <>
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold">{initial ? m.heading(initial.name) : m.newHeading}</h2>
        <Button type="button" variant="ghost" onClick={onClose}>
          {t.common.close}
        </Button>
      </div>
      <Label htmlFor="sched-name">{m.name}</Label>
      <Input id="sched-name" value={name} onChange={(e) => setName(e.target.value)} disabled={!canWrite} />
      <Label htmlFor="sched-template">{m.jobTemplate}</Label>
      <Select
        id="sched-template"
        options={templates.map((t) => ({ value: t.id, label: t.name }))}
        value={templateId}
        onChange={setTemplateId}
        disabled={!canWrite}
      />
      <Label htmlFor="sched-tz">{m.timezone}</Label>
      <Input id="sched-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} disabled={!canWrite} />
      <div role="radiogroup" aria-label={m.when} className="flex gap-1">
        {(['simple', 'cron'] as const).map((md) => (
          <button
            key={md}
            type="button"
            role="radio"
            aria-checked={mode === md}
            className={`rounded-md border px-2.5 py-1 text-xs ${mode === md ? 'border-primary text-primary' : 'border-border text-muted-foreground'}`}
            onClick={() => setMode(md)}
          >
            {md === 'simple' ? m.simple : m.cron}
          </button>
        ))}
      </div>
      {mode === 'simple' ? (
        <div className="flex flex-col gap-2">
          <Label htmlFor="sched-kind">{m.repeat}</Label>
          <Select
            id="sched-kind"
            options={KINDS.map((k) => ({ value: k, label: m.kind[k] }))}
            value={simple.kind}
            onChange={(value) => setKind(value as SimpleKind)}
          />
          {(simple.kind === 'every_minutes' || simple.kind === 'every_hours') && (
            <>
              <Label htmlFor="sched-every">{m.every}</Label>
              <Select
                id="sched-every"
                options={(simple.kind === 'every_minutes' ? MINUTE_STEPS : HOUR_STEPS).map((n) => ({ value: String(n), label: String(n) }))}
                value={String(simple.every)}
                onChange={(value) => setSimple({ ...simple, every: Number(value) })}
              />
            </>
          )}
          {simple.kind === 'weekly' && (
            <div className="flex flex-wrap gap-2">
              {WEEKDAYS.map((d) => (
                <label key={d} className="flex items-center gap-1 text-xs">
                  <input type="checkbox" checked={simple.days.includes(d)} onChange={() => toggleDay(d)} />
                  {m.weekday[d]}
                </label>
              ))}
            </div>
          )}
          {simple.kind === 'monthly' && (
            <>
              <Label htmlFor="sched-day">{m.dayOfMonth}</Label>
              <Input id="sched-day" type="number" min={1} max={31} value={simple.day} onChange={(e) => setSimple({ ...simple, day: Number(e.target.value) })} />
            </>
          )}
          {(simple.kind === 'daily' || simple.kind === 'weekly' || simple.kind === 'monthly') && (
            <>
              <Label htmlFor="sched-at">{m.at}</Label>
              <Input id="sched-at" type="time" value={simple.time} onChange={(e) => setSimple({ ...simple, time: e.target.value })} />
            </>
          )}
        </div>
      ) : (
        <>
          <Label htmlFor="sched-cron">{m.cronExpression}</Label>
          <Input id="sched-cron" className="font-mono" value={cron} onChange={(e) => setCron(e.target.value)} disabled={!canWrite} />
        </>
      )}
      <div className="flex flex-col gap-1 rounded-md border border-border p-2">
        {previewError ? (
          <p role="alert" className="text-xs text-[var(--px-crit)]">{previewError}</p>
        ) : (
          <>
            {preview.data?.description && <p className="text-xs">{preview.data.description}</p>}
            <ol className="flex flex-col gap-0.5 font-mono text-[11px] text-muted-foreground">
              {(preview.data?.next ?? []).map((at, i) => (
                <li key={at} aria-label={m.occurrence(i + 1)}>
                  {zoneFmt(at)}
                </li>
              ))}
            </ol>
          </>
        )}
      </div>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={overlapSkip} onChange={(e) => setOverlapSkip(e.target.checked)} disabled={!canWrite} />
        {m.skipOverlap}
      </label>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={catchUp} onChange={(e) => setCatchUp(e.target.checked)} disabled={!canWrite} />
        {m.catchUp}
      </label>
      {catchUp ? (
        <>
          <Label htmlFor="sched-window">{m.catchUpWindow}</Label>
          <Select id="sched-window" options={WINDOWS.map((w) => ({ value: w, label: w }))} value={catchUpWindow} onChange={setCatchUpWindow} />
        </>
      ) : (
        <p className="text-[11px] text-muted-foreground">{m.otherwise}</p>
      )}
      <Label htmlFor="sched-maintenance">{m.maintenanceGuard}</Label>
      <Select
        id="sched-maintenance"
        options={GUARDS.map((g) => ({ value: g, label: m.maintenance[g] }))}
        value={maintenance}
        onChange={(value) => setMaintenance(value as MaintenanceGuard)}
        disabled={!canWrite}
      />
      <Label htmlFor="sched-limit">{m.limit}</Label>
      <Input id="sched-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder={m.limitPlaceholder} disabled={!canWrite} />
      <Label htmlFor="sched-vars">{m.extraVars}</Label>
      <textarea
        id="sched-vars"
        className="min-h-20 rounded-md border border-border bg-background p-2 font-mono text-xs"
        value={extraVars}
        onChange={(e) => setExtraVars(e.target.value)}
        placeholder='{"release": "v1.2"}'
        disabled={!canWrite}
      />
      {error && <p role="alert" className="text-xs text-[var(--px-crit)]">{error}</p>}
      <div className="flex flex-wrap justify-end gap-2">
        {initial && canRun && (
          <Button type="button" onClick={() => void run()}>
            {m.runNow}
          </Button>
        )}
        {initial && canWrite && (
          <Button type="button" onClick={() => void toggle()}>
            {initial.paused ? m.resume : m.pause}
          </Button>
        )}
        {initial && canWrite && (
          <Button type="button" variant="destructive" onClick={() => void del()}>
            {t.common.delete}
          </Button>
        )}
        {canWrite && (
          <Button type="button" variant="primary" onClick={() => void save()} disabled={create.isPending || update.isPending}>
            {t.common.save}
          </Button>
        )}
      </div>
      {initial && <ScheduleNotifications org={org} scheduleId={initial.id} canWrite={canWrite} />}
      {initial && <ScheduleRuns org={org} id={initial.id} />}
    </>
  );
}
