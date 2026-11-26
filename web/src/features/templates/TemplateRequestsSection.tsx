import { Link } from '@tanstack/react-router';
import { Pill } from '../../ui/badge.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { Card } from '../../ui/table.tsx';
import { useRunRequests } from '../../api/hooks/useRunRequests.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { requestTone } from '../runs/runRequestStatus.ts';
import { shortId } from '../runs/status.ts';
import { openRequests } from './templateDetail.ts';

const COLUMNS = 'minmax(0,1fr) 140px 150px';

/**
 * TemplateRequestsSection is the Overview's "Open run requests": what an
 * assistant asked to run with this template (or to change on its schedules
 * and triggers) that still waits — for a decision, or approved and waiting
 * for its sync. The requests list has no template filter on the server, so
 * the organization's list is narrowed here; the link beside the title opens
 * the requests page narrowed the same way.
 */
export function TemplateRequestsSection({ org, templateId }: { org: string; templateId: string }) {
  const t = useT();
  const fmt = useFormat();
  const o = t.templates.page.overview;
  const r = t.runs.requests;
  const requests = useRunRequests(org);
  const open = openRequests(requests.data?.requests, templateId);

  return (
    <Section
      id="template-requests"
      title={o.requests}
      actions={
        <Link to="/o/$org/runs/requests" params={{ org }} search={{ template: templateId }} className="text-xs text-muted-foreground hover:text-primary">
          {o.allRequests}
        </Link>
      }
    >
      {requests.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {requests.error instanceof Error ? requests.error.message : o.requestsFailed}
        </p>
      ) : !requests.data ? (
        <p className="text-[13px] text-muted-foreground">{t.common.loading}</p>
      ) : open.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">{o.noRequests}</p>
      ) : (
        <Card>
          {open.map((q) => (
            <ListRow key={q.id} to="/o/$org/runs/requests/$id" params={{ org, id: q.id }} columns={COLUMNS} label={q.reason || shortId(q.id)}>
              <ListRowName className="truncate text-[13px]">{q.reason || shortId(q.id)}</ListRowName>
              <span className="truncate text-xs text-muted-foreground">{r.kind[q.kind]}</span>
              <span className="flex flex-col items-start gap-0.5">
                <Pill tone={requestTone(q.status)}>{r.status[q.status]}</Pill>
                {q.status === 'pending' && q.expires_at && <span className="text-[11px] text-muted-foreground">{r.expires(fmt.relative(q.expires_at))}</span>}
              </span>
            </ListRow>
          ))}
        </Card>
      )}
    </Section>
  );
}
