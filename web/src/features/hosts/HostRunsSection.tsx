import { Link } from '@tanstack/react-router';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { Pill } from '../../ui/badge.tsx';
import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { kindLabel, shortId, statusLabel, statusTone } from '../runs/status.ts';
import { triggerLabel } from '../runs/trigger.ts';

/** How many of the host's runs the section shows; the rest are one link away. */
const RECENT = 5;

/**
 * HostRunsSection is the host page's "Runs" section: the latest runs that
 * targeted the host (the jobs list's own `host_id` filter: a run whose
 * target hosts include this one) and, beside the title, a link to the runs
 * page filtered to it.
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
        {rows.length > 0 && (
          <ul>
            {rows.map((j) => (
              <li key={j.id} className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-2.5 text-xs last:border-b-0">
                <Pill tone={statusTone(j.status)}>{statusLabel(j.status, t)}</Pill>
                <Link to="/o/$org/runs/$id" params={{ org, id: j.id }} className="font-mono text-[13px] hover:text-primary">
                  {shortId(j.id)}
                </Link>
                <span className="flex-1 text-muted-foreground">
                  {kindLabel(j.kind, t)} · {triggerLabel(j.trigger, t)}
                </span>
                <span className="text-muted-foreground" title={format.dateTime(j.created_at)}>
                  {format.relative(j.created_at)}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </Section>
  );
}
