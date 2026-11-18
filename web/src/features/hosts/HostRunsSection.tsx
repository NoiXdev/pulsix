import { useState, type ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { DataList } from '../../ui/data-list/index.ts';
import { Section } from '../../ui/detail-layout.tsx';
import { ListToolbar, ResetFilters } from '../../ui/list-toolbar.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListView } from '../lists/useListView.ts';
import { runColumns } from '../runs/runColumns.tsx';
import { RUN_KINDS, RUN_STATUSES } from '../runs/runsSearch.ts';
import { kindLabel, shortId, statusLabel } from '../runs/status.ts';

/** How many of the host's runs the section shows by default; the rest are one link away. */
const RECENT = 5;

const HOST_RUN_DEFAULTS: ReadonlySet<string> = new Set(['run', 'status', 'created']);

/**
 * HostRunsSection is the host page's "Runs" section: a teaser of the latest
 * runs that targeted the host (the jobs list's own `host_id` filter: a run
 * whose target hosts include this one), each a row to its run, and, beside
 * the title, a link to the runs page filtered to it. It is the runs list's
 * columns (runColumns) on a list of its own (id `host-runs`): the person
 * picks them here too, but there is no paging, sorting or URL state: the
 * full, sortable list is one link away. The Overview tab shows fewer
 * (`limit`) and puts its own way to the Runs tab in place of that link
 * (`actions`). Status and kind narrow the section (the jobs list's own
 * filters, kept in the component: it is a section of the host page, not a
 * list with a URL).
 */
export function HostRunsSection({ org, hostId, limit = RECENT, actions }: { org: string; hostId: string; limit?: number; actions?: ReactNode }) {
  const t = useT();
  const fmt = useFormat();
  const [status, setStatus] = useState<string | undefined>();
  const [kind, setKind] = useState<string | undefined>();
  const active = !!status || !!kind;
  const jobs = useJobs(org, { host: hostId, limit, status, kind });
  const rows = (jobs.data?.pages[0]?.jobs ?? []).slice(0, limit);
  // The section is narrow (a third of the host page): by default only run, status and
  // created, which is what the old teaser showed; the others can be switched on.
  const columns = runColumns(t, fmt).map((c) => ({ ...c, defaultVisible: HOST_RUN_DEFAULTS.has(c.id) }));
  const view = useListView('host-runs', columns, { mode: 'client', urlState: false, rowKey: (j) => j.id });
  const { visibleColumns, onVisibleColumnsChange, defaultColumns, density } = view.listProps(undefined, () => '');

  return (
    <Section
      id="runs"
      title={t.hosts.detail.section.runs}
      actions={
        actions ?? (
          <Link
            to="/o/$org/runs"
            params={{ org }}
            search={{ host: hostId, status: undefined, kind: undefined, trigger: undefined, schedule: undefined }}
            className="text-xs text-muted-foreground hover:text-primary"
          >
            {t.hosts.detail.runs.all}
          </Link>
        )
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
          empty={active ? t.hosts.detail.runs.noMatch : t.hosts.detail.runs.none}
          toolbar={
            <ListToolbar>
              <FilterSelect
                label={t.runs.list.filterStatus}
                allLabel={t.runs.list.allStatuses}
                options={[{ value: 'active', label: t.runs.list.statusActive }, ...RUN_STATUSES.map((v) => ({ value: v, label: statusLabel(v, t) }))]}
                value={status}
                onChange={setStatus}
              />
              <FilterSelect
                label={t.runs.list.filterKind}
                allLabel={t.runs.list.allKinds}
                options={RUN_KINDS.map((v) => ({ value: v, label: kindLabel(v, t) }))}
                value={kind}
                onChange={setKind}
              />
              {active && (
                <ResetFilters
                  onReset={() => {
                    setStatus(undefined);
                    setKind(undefined);
                  }}
                />
              )}
            </ListToolbar>
          }
        />
      )}
    </Section>
  );
}
