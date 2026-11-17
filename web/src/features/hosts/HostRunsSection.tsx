import { Link } from '@tanstack/react-router';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { DataList } from '../../ui/data-list/index.ts';
import { Section } from '../../ui/detail-layout.tsx';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { runColumns } from '../runs/runColumns.tsx';
import { shortId } from '../runs/status.ts';

/** How many of the host's runs the section shows; the rest are one link away. */
const RECENT = 5;

/**
 * HostRunsSection is the host page's "Runs" section: a teaser of the latest
 * runs that targeted the host (the jobs list's own `host_id` filter: a run
 * whose target hosts include this one), each a row to its run, and, beside
 * the title, a link to the runs page filtered to it. It is the runs list's
 * columns (runColumns) on a list of its own (id `host-runs`): the person
 * picks them here too, but there is no paging, sorting or URL state: the
 * full, sortable list is one link away.
 */
export function HostRunsSection({ org, hostId }: { org: string; hostId: string }) {
  const t = useT();
  const jobs = useJobs(org, { host: hostId, limit: RECENT });
  const rows = (jobs.data?.pages[0]?.jobs ?? []).slice(0, RECENT);
  const columns = runColumns(t);
  const view = useListView('host-runs', columns, { mode: 'client', urlState: false, rowKey: (j) => j.id });
  const { visibleColumns, onVisibleColumnsChange, defaultColumns, density } = view.listProps(undefined, () => '');

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
      {jobs.isError && (
        <p role="alert" className="py-3 text-xs text-destructive">
          {jobs.error instanceof Error ? jobs.error.message : t.hosts.detail.runs.readFailed}
        </p>
      )}
      {!jobs.isError && (
        <DataList
          label={t.hosts.detail.runs.label}
          columns={columns}
          rows={rows}
          rowKey={(j) => j.id}
          rowLink={(j) => ({ to: '/o/$org/runs/$id', params: { org, id: j.id }, label: shortId(j.id) })}
          visibleColumns={visibleColumns}
          onVisibleColumnsChange={onVisibleColumnsChange}
          defaultColumns={defaultColumns}
          density={density}
          loading={!jobs.data || !view.ready}
          empty={t.hosts.detail.runs.none}
        />
      )}
    </Section>
  );
}
