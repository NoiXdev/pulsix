import type { Job } from '../../api/types.ts';
import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { kindLabel, shortId, statusLabel, statusTone } from './status.ts';
import { triggerLabel } from './trigger.ts';

/**
 * runColumns is every column the runs list offers (list id `runs`). The
 * server sorts by created (the default, newest first), status, kind and
 * trigger; the template is not sortable (the list carries only its id).
 * "Created" is the run's created_at: when it was asked for, not when a
 * worker started it; it reads as a relative time with the full date as title.
 */
export function runColumns(t: Messages, fmt: Pick<Format, 'relative' | 'dateTime'>): DataListColumn<Job>[] {
  const c = t.runs.list.columns;
  return [
    {
      id: 'run',
      label: c.run,
      width: '90px',
      required: true,
      defaultVisible: true,
      render: (j) => <span className="font-mono">{shortId(j.id)}</span>,
    },
    {
      id: 'kind',
      label: c.kind,
      width: '120px',
      sortKey: 'kind',
      defaultVisible: true,
      render: (j) => <span>{kindLabel(j.kind, t)}</span>,
    },
    {
      id: 'status',
      label: c.status,
      width: '110px',
      sortKey: 'status',
      defaultVisible: true,
      render: (j) => (
        <span>
          <Pill tone={statusTone(j.status)}>{statusLabel(j.status, t)}</Pill>
        </span>
      ),
    },
    {
      id: 'trigger',
      label: c.trigger,
      width: '100px',
      sortKey: 'trigger',
      defaultVisible: true,
      render: (j) => <span>{triggerLabel(j.trigger, t)}</span>,
    },
    {
      id: 'template',
      label: c.template,
      width: 'minmax(0,0.7fr)',
      defaultVisible: true,
      render: (j) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{j.template_id ? shortId(j.template_id) : '—'}</span>,
    },
    {
      id: 'zone',
      label: c.zone,
      width: '90px',
      defaultVisible: true,
      render: (j) => <span className={`font-mono text-muted-foreground ${CELL_MINOR}`}>{shortId(j.zone_id)}</span>,
    },
    {
      id: 'created',
      label: c.created,
      width: '130px',
      sortKey: 'created',
      defaultVisible: true,
      render: (j) => (
        <span className={`text-muted-foreground ${CELL_MINOR}`} title={fmt.dateTime(j.created_at)}>
          {fmt.relative(j.created_at)}
        </span>
      ),
    },
  ];
}
