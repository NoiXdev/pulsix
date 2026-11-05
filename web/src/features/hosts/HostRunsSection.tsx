import { Link } from '@tanstack/react-router';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { Pill } from '../../ui/badge.tsx';
import { Card } from '../../ui/table.tsx';
import { ListRow } from '../../ui/list-row.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { kindLabel, shortId, statusLabel, statusTone } from '../runs/status.ts';
import { triggerLabel } from '../runs/trigger.ts';

/** How many of the host's runs the section shows; the rest are one link away. */
const RECENT = 5;

const COLUMNS = '110px 80px minmax(0, 1fr) 110px';

/**
 * HostRunsSection is the host page's "Runs" section: the latest runs that
 * targeted the host (the jobs list's own `host_id` filter: a run whose
 * target hosts include this one), each a row to its run, and, beside the
 * title, a link to the runs page filtered to it.
 */
export function HostRunsSection({ org, hostId }: { org: string; hostId: string }) {
  const t = useT();
  const format = useFormat();
  const jobs = useJobs(org, { host: hostId, limit: RECENT });
  const rows = (jobs.data?.pages[0]?.jobs ?? []).slice(0, RECENT);

  return (
    <Section
      id="runs"
      title={t.hosts.detail.section.runs}
      actions={
        <Link
          to="/o/$org/runs"
          params={{ org }}
          search={{ host: hostId, status: undefined, kind: undefined, trigger: undefined, schedule: undefined }}
          className="text-xs text-muted-foreground hover:text-primary"
        >
          {t.hosts.detail.runs.all}
        </Link>
      }
    >
      <Card>
        {jobs.isError && (
          <p role="alert" className="px-4 py-3 text-xs text-destructive">
            {jobs.error instanceof Error ? jobs.error.message : t.hosts.detail.runs.readFailed}
          </p>
        )}
        {!jobs.data && !jobs.isError && <p className="px-4 py-3 text-xs text-muted-foreground">{t.common.loading}</p>}
        {jobs.data && rows.length === 0 && <p className="px-4 py-3 text-xs text-muted-foreground">{t.hosts.detail.runs.none}</p>}
        {rows.map((j) => (
          <ListRow key={j.id} to="/o/$org/runs/$id" params={{ org, id: j.id }} columns={COLUMNS} label={shortId(j.id)}>
            <span>
              <Pill tone={statusTone(j.status)}>{statusLabel(j.status, t)}</Pill>
            </span>
            <span className="font-mono text-[13px]">{shortId(j.id)}</span>
            <span className="truncate text-xs text-muted-foreground">
              {kindLabel(j.kind, t)} · {triggerLabel(j.trigger, t)}
            </span>
            <span className="text-xs text-muted-foreground" title={format.dateTime(j.created_at)}>
              {format.relative(j.created_at)}
            </span>
          </ListRow>
        ))}
      </Card>
    </Section>
  );
}
