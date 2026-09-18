import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
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
import { ScheduleRuns } from './ScheduleRuns.tsx';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';
const WINDOWS = ['15m', '30m', '1h', '2h', '6h', '12h', '24h'];
const KIND_LABEL: Record<SimpleKind, string> = {
  every_minutes: 'Every N minutes',
  every_hours: 'Every N hours',
  daily: 'Daily',
  weekly: 'Weekly',
  monthly: 'Monthly',
};

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
  const isNew = props.id === 'new';
  const detail = useSchedule(props.org, isNew ? '' : props.id);
  const initial = isNew ? undefined : detail.data?.schedule;
  return (
    <section aria-label="Schedule editor" className="flex flex-col gap-3 rounded-xl border border-border bg-card p-4">
      {!isNew && !initial ? <QueryLoading /> : <EditorForm key={initial ? `${initial.id}-${initial.revision}` : 'new'} {...props} initial={initial} />}
    </section>
  );
}

function EditorForm({ org, templates, defaultTemplate, canWrite, canRun, onClose, initial }: ScheduleEditorProps & { initial?: Schedule }) {
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
  const [window, setWindow] = useState(initial?.guards.catch_up?.window || '1h');
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
        setError('Extra vars must be a JSON object.');
        return null;
      }
    }
    return {
      template_id: templateId,
      name,
      mode,
      ...(mode === 'simple' ? { simple: toSimpleBody(simple) } : { cron }),
      timezone,
      guards: { overlap: overlapSkip ? 'skip' : 'queue', catch_up: { enabled: catchUp, window } },
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
      toast.show('Schedule saved');
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        setError('This schedule changed since you loaded it — reload and apply your change again.');
      } else {
        setError(err instanceof Error ? err.message : 'Saving failed');
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
      toast.show(err instanceof Error ? err.message : 'Starting the run failed', 'destructive');
    }
  }

  async function del() {
    if (!initial || !confirm(`Delete schedule ${initial.name}?`)) return;
    await remove.mutateAsync(initial.id);
    onClose();
  }

  const zoneFmt = (at: string) => new Date(at).toLocaleString(undefined, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short' });
  const previewError = preview.error instanceof Error ? preview.error.message : null;

  return (
    <>
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold">{initial ? `Schedule · ${initial.name}` : 'New schedule'}</h2>
        <Button type="button" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      <Label htmlFor="sched-name">Name</Label>
      <Input id="sched-name" value={name} onChange={(e) => setName(e.target.value)} disabled={!canWrite} />
      <Label htmlFor="sched-template">Job template</Label>
      <select id="sched-template" className={SELECT} value={templateId} onChange={(e) => setTemplateId(e.target.value)} disabled={!canWrite}>
        {templates.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </select>
      <Label htmlFor="sched-tz">Timezone</Label>
      <Input id="sched-tz" className="font-mono" value={timezone} onChange={(e) => setTimezone(e.target.value)} disabled={!canWrite} />
      <div role="radiogroup" aria-label="When" className="flex gap-1">
        {(['simple', 'cron'] as const).map((m) => (
          <button
            key={m}
            type="button"
            role="radio"
            aria-checked={mode === m}
            className={`rounded-md border px-2.5 py-1 text-xs ${mode === m ? 'border-primary text-primary' : 'border-border text-muted-foreground'}`}
            onClick={() => setMode(m)}
          >
            {m === 'simple' ? 'Simple' : 'Cron'}
          </button>
        ))}
      </div>
      {mode === 'simple' ? (
        <div className="flex flex-col gap-2">
          <Label htmlFor="sched-kind">Repeat</Label>
          <select id="sched-kind" className={SELECT} value={simple.kind} onChange={(e) => setKind(e.target.value as SimpleKind)}>
            {(Object.keys(KIND_LABEL) as SimpleKind[]).map((k) => (
              <option key={k} value={k}>
                {KIND_LABEL[k]}
              </option>
            ))}
          </select>
          {(simple.kind === 'every_minutes' || simple.kind === 'every_hours') && (
            <>
              <Label htmlFor="sched-every">Every</Label>
              <select id="sched-every" className={SELECT} value={simple.every} onChange={(e) => setSimple({ ...simple, every: Number(e.target.value) })}>
                {(simple.kind === 'every_minutes' ? MINUTE_STEPS : HOUR_STEPS).map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </>
          )}
          {simple.kind === 'weekly' && (
            <div className="flex flex-wrap gap-2">
              {WEEKDAYS.map((d) => (
                <label key={d} className="flex items-center gap-1 text-xs">
                  <input type="checkbox" checked={simple.days.includes(d)} onChange={() => toggleDay(d)} />
                  {d[0].toUpperCase() + d.slice(1)}
                </label>
              ))}
            </div>
          )}
          {simple.kind === 'monthly' && (
            <>
              <Label htmlFor="sched-day">Day of month</Label>
              <Input id="sched-day" type="number" min={1} max={31} value={simple.day} onChange={(e) => setSimple({ ...simple, day: Number(e.target.value) })} />
            </>
          )}
          {(simple.kind === 'daily' || simple.kind === 'weekly' || simple.kind === 'monthly') && (
            <>
              <Label htmlFor="sched-at">At</Label>
              <Input id="sched-at" type="time" value={simple.time} onChange={(e) => setSimple({ ...simple, time: e.target.value })} />
            </>
          )}
        </div>
      ) : (
        <>
          <Label htmlFor="sched-cron">Cron expression</Label>
          <Input id="sched-cron" className="font-mono" value={cron} onChange={(e) => setCron(e.target.value)} disabled={!canWrite} />
        </>
      )}
      <div className="flex flex-col gap-1 rounded-md border border-border p-2">
        {previewError ? (
          <p className="text-xs text-[var(--px-crit)]">{previewError}</p>
        ) : (
          <>
            {preview.data?.description && <p className="text-xs">{preview.data.description}</p>}
            <ol className="flex flex-col gap-0.5 font-mono text-[11px] text-muted-foreground">
              {(preview.data?.next ?? []).map((at, i) => (
                <li key={at} aria-label={`occurrence ${i + 1}`}>
                  {zoneFmt(at)}
                </li>
              ))}
            </ol>
          </>
        )}
      </div>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={overlapSkip} onChange={(e) => setOverlapSkip(e.target.checked)} disabled={!canWrite} />
        Skip when a run of this template is still active
      </label>
      <label className="flex items-center gap-2 text-xs">
        <input type="checkbox" checked={catchUp} onChange={(e) => setCatchUp(e.target.checked)} disabled={!canWrite} />
        Catch up a missed run after downtime
      </label>
      {catchUp ? (
        <>
          <Label htmlFor="sched-window">Catch-up window</Label>
          <select id="sched-window" className={SELECT} value={window} onChange={(e) => setWindow(e.target.value)}>
            {WINDOWS.map((w) => (
              <option key={w} value={w}>
                {w}
              </option>
            ))}
          </select>
        </>
      ) : (
        <p className="text-[11px] text-muted-foreground">otherwise the next regular run happens</p>
      )}
      <Label htmlFor="sched-limit">Limit</Label>
      <Input id="sched-limit" className="font-mono" value={limit} onChange={(e) => setLimit(e.target.value)} placeholder="the template's limit" disabled={!canWrite} />
      <Label htmlFor="sched-vars">Extra vars</Label>
      <textarea
        id="sched-vars"
        className="min-h-20 rounded-md border border-border bg-background p-2 font-mono text-xs"
        value={extraVars}
        onChange={(e) => setExtraVars(e.target.value)}
        placeholder='{"release": "v1.2"}'
        disabled={!canWrite}
      />
      {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
      <div className="flex flex-wrap justify-end gap-2">
        {initial && canRun && (
          <Button type="button" onClick={() => void run()}>
            Run now
          </Button>
        )}
        {initial && canWrite && (
          <Button type="button" onClick={() => void (initial.paused ? resume : pause).mutateAsync(initial.id)}>
            {initial.paused ? 'Resume' : 'Pause'}
          </Button>
        )}
        {initial && canWrite && (
          <Button type="button" variant="destructive" onClick={() => void del()}>
            Delete
          </Button>
        )}
        {canWrite && (
          <Button type="button" variant="primary" onClick={() => void save()} disabled={create.isPending || update.isPending}>
            Save
          </Button>
        )}
      </div>
      {initial && <ScheduleRuns org={org} id={initial.id} />}
    </>
  );
}
