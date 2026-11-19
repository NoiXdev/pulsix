import { useNavigate, useParams } from '@tanstack/react-router';
import { FilePlus2 } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { playbookColumns, playbookLabel } from './playbookColumns.tsx';
import { PLAYBOOK_SOURCES, PLAYBOOKS_FILTERS, filterPlaybooks } from './playbooksSearch.ts';

/** PlaybooksPage lists the playbooks jobs run against (spec §7, backlog
 * 2.9): repo-sourced ones (a path inside a registered repository, arriving
 * with a sync) and editor ones (an inline YAML document). Each row opens
 * the playbook's page; "New playbook" opens the new page (backlog 3.123). */
export function PlaybooksPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const orgInfo = useOrg(org);
  const playbooks = usePlaybooks(org);
  const repos = useRepos(org);
  const canWrite = may(orgInfo.data?.org.role, 'playbooks.write');

  const columns = playbookColumns({ t, repoName: (id) => (repos.data?.repos ?? []).find((r) => r.id === id)?.name });
  const { filters, active, setFilter, reset } = useListFilters(PLAYBOOKS_FILTERS);
  const view = useListView('playbooks', columns, { mode: 'client', rowKey: (p) => p.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(playbooks);
  if (guard) return guard;
  if (!playbooks.data) return <QueryLoading />;
  const list = playbooks.data.playbooks ?? [];
  const shown = filterPlaybooks(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.playbooks.list.filters;
  // Only repositories that hold a playbook are offered; one only the URL names stays so it can be cleared.
  const repoIds = [...new Set([...list.map((p) => p.repo_id).filter((id): id is string => !!id), ...(filters.repo ? [filters.repo] : [])])];
  const repoOptions = repoIds
    .map((id) => ({ value: id, label: (repos.data?.repos ?? []).find((r) => r.id === id)?.name ?? id.slice(0, 8) }))
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.playbooks.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.playbooks.list.summary(list.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => void navigate({ to: '/o/$org/playbooks/new', params: { org } })}>
            <FilePlus2 size={15} /> {t.playbooks.list.add}
          </Button>
        )}
      </div>
      <DataList
        label={t.playbooks.list.label}
        columns={columns}
        rows={rows}
        rowKey={(p) => p.id}
        rowLink={(p) => ({ to: '/o/$org/playbooks/$id', params: { org, id: p.id }, search: { tab: undefined }, label: playbookLabel(p, t) })}
        loading={!view.ready}
        empty={active ? t.playbooks.list.noMatch : t.playbooks.list.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={t.playbooks.list.count} />}>
            <SearchInput
              label={t.playbooks.list.search}
              placeholder={t.playbooks.list.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.source}
              allLabel={f.allSources}
              options={PLAYBOOK_SOURCES.map((v) => ({ value: v, label: v === 'repo' ? t.playbooks.list.sourceRepo : t.playbooks.list.sourceEditor }))}
              value={filters.source}
              onChange={(v) => setFilter('source', v as typeof filters.source)}
            />
            <FilterSelect label={f.repo} allLabel={f.allRepos} options={repoOptions} value={filters.repo} onChange={(v) => setFilter('repo', v)} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.playbooks.list.count)}
      />
    </>
  );
}
