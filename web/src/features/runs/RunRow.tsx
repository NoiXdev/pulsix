import { useNavigate } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { ListRow, ListRowName, RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import type { Job } from '../../api/types.ts';
import { isCancellable, kindLabel, shortId, statusLabel, statusTone } from './status.ts';
import { triggerLabel } from './trigger.ts';
import { useCancelRun } from './useCancelRun.ts';
import { useT } from '../../i18n/useT.ts';

/**
 * RunRow is one run in the runs list: the whole row opens the run, and its
 * ⋯ menu offers Open and, for a live run and a role that may run jobs,
 * Cancel run (no confirmation, as on the run page). Retry stays on the run
 * page: the list does not carry the per-host stats it depends on. Reserve a
 * 40px last track in `columns` for the menu.
 */
export function RunRow({ org, job, columns, canRun }: { org: string; job: Job; columns: string; canRun: boolean }) {
  const t = useT();
  const navigate = useNavigate();
  const cancel = useCancelRun(org, job.id);
  const id = shortId(job.id);

  return (
    <ListRow
      to="/o/$org/runs/$id"
      params={{ org, id: job.id }}
      columns={columns}
      label={id}
      menu={
        <RowMenu label={t.common.rowActions(id)}>
          <RowMenuItem onSelect={() => void navigate({ to: '/o/$org/runs/$id', params: { org, id: job.id } })}>{t.common.open}</RowMenuItem>
          {canRun && isCancellable(job.status) && (
            <RowMenuItem onSelect={() => void cancel.cancel()} disabled={cancel.isPending}>
              {t.runs.list.cancelItem}
            </RowMenuItem>
          )}
        </RowMenu>
      }
    >
      <ListRowName className="font-mono text-[13px]">{id}</ListRowName>
      <span className="text-xs">{kindLabel(job.kind, t)}</span>
      <span>
        <Pill tone={statusTone(job.status)}>{statusLabel(job.status, t)}</Pill>
      </span>
      <span className="text-xs">{triggerLabel(job.trigger, t)}</span>
      <span className="truncate font-mono text-xs text-muted-foreground">{job.template_id ? shortId(job.template_id) : '—'}</span>
      <span className="font-mono text-xs text-muted-foreground">{shortId(job.zone_id)}</span>
      <span className="text-xs text-muted-foreground">{job.created_at.slice(0, 16).replace('T', ' ')}</span>
    </ListRow>
  );
}
