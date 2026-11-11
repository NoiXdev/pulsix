import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { Clock, Pause } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { usePauseAllSchedules, useSchedules } from '../../api/hooks/useSchedules.ts';
import { may } from '../../lib/roles.ts';
import { ScheduleCalendar } from '../calendar/ScheduleCalendar.tsx';
import { ScheduleEditor } from './ScheduleEditor.tsx';
import { OUTCOME_TONE, outcomeLabel } from './outcome.ts';
import { CATEGORY_TONE, type Category } from './notifyCategory.ts';
import { useT } from '../../i18n/useT.ts';
import { relative } from './simple.ts';
import { useConfirm } from '../../ui/confirmContext.ts';

const COLUMNS = '90px minmax(0, 1.2fr) minmax(0, 1fr) 90px 150px 90px';

/** SchedulesPage is the Scheduler artboard (spec 2026-09-18 §2): every
 * schedule of the organization with status, template, timetable, next run
 * and last result; "Pause all"; "New schedule"; the detail panel for the
 * selected one (?id=), optionally filtered to one template (?template=). */
export function SchedulesPage() {
  const t = useT();
  const m = t.schedules;
  const { org } = useParams({ strict: false }) as { org: string };
  const search = useSearch({ strict: false }) as { id?: string; template?: string };
  const navigate = useNavigate();
  const toast = useToast();
  const ask = useConfirm();
  const orgInfo = useOrg(org);
  const canWrite = may(orgInfo.data?.org.role, 'templates.write');
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const schedules = useSchedules(org, search.template);
  const templates = useTemplates(org);
  const pauseAll = usePauseAllSchedules(org);

  const guard = useQueryGuard(schedules);
  if (guard) return guard;
  if (!schedules.data) return <QueryLoading />;
  const list = schedules.data.schedules ?? [];
  const templateList = templates.data?.templates ?? [];
  const templateName = (id: string) => templateList.find((t) => t.id === id)?.name ?? id.slice(0, 8);
  const now = new Date();

  const select = (id?: string) => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id, template: search.template } });

  async function onPauseAll() {
    if (!(await ask({ message: m.pauseAllConfirm, confirmLabel: m.pauseAll }))) return;
    try {
      const res = await pauseAll.mutateAsync();
      toast.show(m.pausedCount(res.paused));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : m.pauseFailed, 'destructive');
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{m.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {m.summary(list.length, list.filter((s) => !s.paused).length)}
          </span>
        </div>
        {canWrite && (
          <div className="flex gap-2">
            <Button type="button" onClick={() => void onPauseAll()}>
              <Pause size={14} /> {m.pauseAll}
            </Button>
            <Button type="button" variant="primary" onClick={() => select('new')}>
              <Clock size={14} /> {m.newSchedule}
            </Button>
          </div>
        )}
      </div>
      {search.template && (
        <div className="flex items-center gap-2 text-xs">
          <span>{m.template(templateName(search.template))}</span>
          <button type="button" className="text-primary" onClick={() => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: undefined, template: undefined } })}>
            {m.showAll}
          </button>
        </div>
      )}
      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_420px]">
        <Card>
          <GridHeader columns={COLUMNS}>
            <span>{m.columns.status}</span>
            <span>{m.columns.schedule}</span>
            <span>{m.columns.cron}</span>
            <span>{m.columns.next}</span>
            <span>{m.columns.notify}</span>
            <span>{m.columns.last}</span>
          </GridHeader>
          {list.map((s) => (
            <div key={s.id} data-row className={s.paused ? 'opacity-55' : undefined}>
              <GridRow columns={COLUMNS}>
                <span>{s.paused ? <Pill tone="unknown">{m.paused}</Pill> : <Pill tone="ok">{m.active}</Pill>}</span>
                <span className="flex min-w-0 flex-col">
                  <button type="button" className="truncate text-left text-[13px] font-medium hover:text-primary" onClick={() => select(s.id)}>
                    {s.name}
                  </button>
                  <span className="truncate font-mono text-[11px] text-muted-foreground">{templateName(s.template_id)}</span>
                </span>
                <span className="flex min-w-0 flex-col">
                  <span className="truncate font-mono text-xs">{s.cron}</span>
                  {s.description && <span className="truncate text-[11px] text-muted-foreground">{s.description}</span>}
                </span>
                <span className="text-xs text-muted-foreground">{s.paused ? '—' : relative(s.next_run_at, now, t)}</span>
                <span className="flex flex-wrap gap-1">
                  {(s.notify_on ?? []).length === 0 ? (
                    <span className="text-xs text-muted-foreground">—</span>
                  ) : (
                    (s.notify_on as Category[]).map((c) => (
                      <span key={c} title={m.categoryLabel[c]}>
                        <Pill tone={CATEGORY_TONE[c]}>{m.category[c]}</Pill>
                      </span>
                    ))
                  )}
                </span>
                <span>{s.last_run ? <Pill tone={OUTCOME_TONE[s.last_run.outcome] ?? 'unknown'}>{outcomeLabel(s.last_run.outcome, t)}</Pill> : <span className="text-xs text-muted-foreground">—</span>}</span>
              </GridRow>
            </div>
          ))}
          {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{m.empty}</p>}
        </Card>
        <div className="lg:col-start-1">
          <ScheduleCalendar org={org} />
        </div>
        {search.id && (
          <ScheduleEditor
            key={search.id}
            org={org}
            id={search.id}
            templates={templateList}
            defaultTemplate={search.template}
            canWrite={canWrite}
            canRun={canRun}
            onClose={() => select(undefined)}
          />
        )}
      </div>
    </>
  );
}
