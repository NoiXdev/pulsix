import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Schedule } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { CATEGORY_TONE, type Category } from './notifyCategory.ts';
import { OUTCOME_TONE, outcomeLabel } from './outcome.ts';
import { relative } from './simple.ts';

export interface ScheduleColumnsContext {
  t: Messages;
  templateName: (id: string) => string;
  /** The moment "Next" counts from. */
  now: Date;
}

/**
 * scheduleColumns is every column the schedules list offers (list id
 * `schedules`), sorted in the browser. The name column is the required
 * one and its row link opens the schedule's editor (?id=). Status is
 * read-only here: Pause and Resume are the row menu's (useScheduleActions).
 */
export function scheduleColumns({ t, templateName, now }: ScheduleColumnsContext): DataListColumn<Schedule>[] {
  const m = t.schedules;
  return [
    {
      id: 'schedule',
      label: m.columns.schedule,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'schedule',
      sortValue: (s) => s.name,
      required: true,
      defaultVisible: true,
      render: (s) => <span className="block truncate font-medium">{s.name}</span>,
    },
    {
      id: 'status',
      label: m.columns.status,
      width: '110px',
      sortKey: 'status',
      sortValue: (s) => s.paused,
      defaultVisible: true,
      render: (s) => (
        <span>
          {s.paused_reason ? (
            <Pill tone="warn">{m.needsReview}</Pill>
          ) : s.paused ? (
            <Pill tone="unknown">{m.paused}</Pill>
          ) : (
            <Pill tone="ok">{m.active}</Pill>
          )}
        </span>
      ),
    },
    {
      id: 'template',
      label: m.columns.template,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'template',
      sortValue: (s) => templateName(s.template_id),
      defaultVisible: true,
      render: (s) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{templateName(s.template_id)}</span>,
    },
    {
      id: 'cron',
      label: m.columns.cron,
      width: 'minmax(0, 1fr)',
      sortKey: 'cron',
      sortValue: (s) => s.cron,
      defaultVisible: true,
      render: (s) => (
        <span className="block truncate">
          <span className="font-mono">{s.cron}</span>
          {s.description && <span className={`text-muted-foreground ${CELL_MINOR}`}> · {s.description}</span>}
        </span>
      ),
    },
    {
      id: 'next',
      label: m.columns.next,
      width: '90px',
      sortKey: 'next',
      sortValue: (s) => (s.paused ? null : s.next_run_at),
      defaultVisible: true,
      render: (s) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{s.paused ? '—' : relative(s.next_run_at, now, t)}</span>,
    },
    {
      id: 'notify',
      label: m.columns.notify,
      width: '150px',
      sortKey: 'notify',
      sortValue: (s) => (s.notify_on ?? []).length,
      defaultVisible: true,
      render: (s) => (
        <span className="flex flex-wrap gap-1">
          {(s.notify_on ?? []).length === 0 ? (
            <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>
          ) : (
            (s.notify_on as Category[]).map((c) => (
              <span key={c} title={m.categoryLabel[c]}>
                <Pill tone={CATEGORY_TONE[c]}>{m.category[c]}</Pill>
              </span>
            ))
          )}
        </span>
      ),
    },
    {
      id: 'last',
      label: m.columns.last,
      width: '90px',
      sortKey: 'last',
      sortValue: (s) => s.last_run?.outcome ?? null,
      defaultVisible: true,
      render: (s) =>
        s.last_run ? (
          <span>
            <Pill tone={OUTCOME_TONE[s.last_run.outcome] ?? 'unknown'}>{outcomeLabel(s.last_run.outcome, t)}</Pill>
          </span>
        ) : (
          <span className={`text-muted-foreground ${CELL_MINOR}`}>—</span>
        ),
    },
  ];
}
