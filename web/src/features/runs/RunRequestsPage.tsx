import { useParams } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { Card, GridHeader } from '../../ui/table.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useRunRequests } from '../../api/hooks/useRunRequests.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { requestTone, sortRequests, useNow } from './runRequestStatus.ts';
import { shortId } from './status.ts';

const COLUMNS = '170px minmax(0, 1fr) minmax(0, 0.6fr) 150px 150px';

/**
 * RunRequestsPage lists the runs an assistant asked for (spec 2026-11-16,
 * "Run requests"): the pending ones first, soonest to expire on top with a
 * countdown, then the decided and expired ones with their status. Each row
 * opens the request, where a person with jobs.run approves or rejects it.
 */
export function RunRequestsPage() {
  const t = useT();
  const r = t.runs.requests;
  const fmt = useFormat();
  const now = useNow();
  const { org } = useParams({ strict: false }) as { org: string };
  const requests = useRunRequests(org);
  const templates = useTemplates(org);

  const guard = useQueryGuard(requests);
  if (guard) return guard;
  if (!requests.data) return <QueryLoading />;

  const rows = sortRequests(requests.data.requests ?? []);
  const pending = rows.filter((q) => q.status === 'pending').length;
  const templateNames = new Map((templates.data?.templates ?? []).map((tpl) => [tpl.id, tpl.name]));

  return (
    <>
      <div className="flex flex-col gap-2">
        <h1 className="text-xl font-semibold">{r.title}</h1>
        <span className="text-[13px] text-muted-foreground">{r.summary}</span>
        <span className="text-[13px] text-muted-foreground">{r.pendingCount(pending)}</span>
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{r.columns.status}</span>
          <span>{r.columns.reason}</span>
          <span>{r.columns.template}</span>
          <span>{r.columns.kind}</span>
          <span>{r.columns.requested}</span>
        </GridHeader>
        {rows.map((q) => (
          <ListRow key={q.id} to="/o/$org/runs/requests/$id" params={{ org, id: q.id }} columns={COLUMNS} label={q.reason || shortId(q.id)}>
            <span className="flex flex-col items-start gap-1">
              <Pill tone={requestTone(q.status)}>{r.status[q.status]}</Pill>
              {q.status === 'pending' && (
                <span className="text-[11px] text-muted-foreground">{r.expires(fmt.relative(q.expires_at, now))}</span>
              )}
            </span>
            <ListRowName className="truncate text-[13px]">{q.reason}</ListRowName>
            <span className="truncate font-mono text-xs">{templateNames.get(q.template_id) ?? shortId(q.template_id)}</span>
            <span className="text-xs text-muted-foreground">{r.kind[q.kind]}</span>
            <span className="font-mono text-[11px] text-muted-foreground">{fmt.dateTime(q.created_at)}</span>
          </ListRow>
        ))}
        {rows.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{r.empty}</p>}
      </Card>
    </>
  );
}
