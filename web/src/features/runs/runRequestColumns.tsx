import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';
import type { Messages } from '../../i18n/types.ts';
import { requestTone } from './runRequestStatus.ts';
import { shortId } from './status.ts';

export interface RunRequestColumnsContext {
  t: Messages;
  templateName: (id: string) => string;
  /** A date and time in the page's language. */
  dateTime: (iso: string) => string;
  /** The distance from now to a moment ("in 30 minutes"). */
  relative: (iso: string) => string;
}

/**
 * runRequestColumns is every column the run requests list offers (list id
 * `run-requests`), sorted in the browser. The reason is the name column and
 * its row link, named by the reason too, opens the request (DataRow leaves
 * the link undescribed, as the cell repeats its label). A pending request
 * keeps its pill and its expiry countdown in the status column; the list's
 * own order (the pending ones first, soonest to expire on top) is what an
 * unsorted list shows.
 */
export function runRequestColumns({ t, templateName, dateTime, relative }: RunRequestColumnsContext): DataListColumn<RunRequest>[] {
  const r = t.runs.requests;
  return [
    {
      id: 'reason',
      label: r.columns.reason,
      width: 'minmax(0, 1.2fr)',
      sortKey: 'reason',
      sortValue: (q) => q.reason || shortId(q.id),
      required: true,
      defaultVisible: true,
      render: (q) => <span className="block truncate">{q.reason || shortId(q.id)}</span>,
    },
    {
      id: 'status',
      label: r.columns.status,
      width: '170px',
      sortKey: 'status',
      sortValue: (q) => q.status,
      defaultVisible: true,
      render: (q) => (
        <span className="flex flex-col items-start gap-1">
          <Pill tone={requestTone(q.status)}>{r.status[q.status]}</Pill>
          {q.status === 'pending' && <span className={`text-muted-foreground ${CELL_MINOR}`}>{r.expires(relative(q.expires_at))}</span>}
        </span>
      ),
    },
    {
      id: 'template',
      label: r.columns.template,
      width: 'minmax(0, 0.6fr)',
      sortKey: 'template',
      sortValue: (q) => templateName(q.template_id),
      defaultVisible: true,
      render: (q) => <span className="block truncate font-mono">{templateName(q.template_id)}</span>,
    },
    {
      id: 'kind',
      label: r.columns.kind,
      width: '150px',
      sortKey: 'kind',
      sortValue: (q) => q.kind,
      defaultVisible: true,
      render: (q) => <span className={`text-muted-foreground ${CELL_MINOR}`}>{r.kind[q.kind]}</span>,
    },
    {
      id: 'requested',
      label: r.columns.requested,
      width: '150px',
      sortKey: 'requested',
      sortValue: (q) => q.created_at,
      defaultVisible: true,
      render: (q) => <span className={`font-mono text-muted-foreground ${CELL_MINOR}`}>{dateTime(q.created_at)}</span>,
    },
  ];
}
