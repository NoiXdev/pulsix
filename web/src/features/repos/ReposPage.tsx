import { useParams } from '@tanstack/react-router';
import { GitBranch } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import { may } from '../../lib/roles.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { repoColumns } from './repoColumns.tsx';
import { REPOS_FILTERS, SYNC_STATES, filterRepos } from './reposSearch.ts';
import { useRepoListActions } from './useRepoListActions.tsx';

/** ReposPage is the git repository registry (spec §7, backlog 2.8; list id
 * `repos`): the checkouts playbooks come from, each with its credential,
 * branch, subpath and the last sync's outcome. The whole registry is one
 * request, sorted and paged in the browser. Writes are operator actions
 * (Sync, Triggers, Edit and Delete are the row menu's); a viewer gets the
 * read-only list. */
export function ReposPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const repos = useRepos(org);
  const canWrite = may(orgInfo.data?.org.role, 'repos.write');
  const canReadCredentials = may(orgInfo.data?.org.role, 'credentials.read');
  // Gates RepoDialog's two credential pickers' own create rows:
  // credentials.write is admin, stricter than repos.write (v0.4g Task 7
  // fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');
  const credentials = useCredentials(org, { enabled: !!orgInfo.data && canReadCredentials });
  const names = credentials.data?.credentials ?? [];

  const actions = useRepoListActions(org, { canWrite, credentials: names, canCreateCredential });
  const columns = repoColumns({ t, credentialName: (id) => credentialName(names, id, t) });
  const { filters, active, setFilter, reset } = useListFilters(REPOS_FILTERS);
  const view = useListView('repos', columns, { mode: 'client', rowKey: (r) => r.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(repos);
  if (guard) return guard;
  if (!repos.data) return <QueryLoading />;
  const list = repos.data.repos ?? [];
  const shown = filterRepos(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.repos.list.filters;
  // A credential only the URL names (a stale link) stays pickable so it can be cleared.
  const credentialIds = [...new Set(list.map((r) => r.credential_id).filter((id): id is string => !!id))];
  const credentialOptions = [...new Set([...credentialIds, ...(filters.credential ? [filters.credential] : [])])]
    .map((id) => ({ value: id, label: credentialName(names, id, t) }))
    .sort((a, b) => a.label.localeCompare(b.label));

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.repos.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">{t.repos.list.summary(list.length)}</span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew}>
            <GitBranch size={15} /> {t.repos.list.add}
          </Button>
        )}
      </div>
      <DataList
        label={t.repos.list.label}
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
        rowLabel={(r) => r.name}
        rowActions={actions.rowActions}
        loading={!view.ready}
        empty={active ? t.repos.list.noMatch : t.repos.list.empty}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={list.length} noun={t.repos.list.count} /> : undefined}>
            <SearchInput
              label={t.repos.list.search}
              placeholder={t.repos.list.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect
              label={f.sync}
              allLabel={f.anySync}
              options={SYNC_STATES.map((s) => ({ value: s, label: f.syncState[s] }))}
              value={filters.sync}
              onChange={(v) => setFilter('sync', v as typeof filters.sync)}
            />
            <FilterSelect label={f.credential} allLabel={f.allCredentials} options={credentialOptions} value={filters.credential} onChange={(v) => setFilter('credential', v)} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.repos.list.count)}
      />
      {actions.dialogs}
    </>
  );
}

function credentialName(credentials: readonly { id: string; name: string; kind: string }[], id: string | undefined, t: Messages): string {
  if (!id) return '—';
  const c = credentials.find((x) => x.id === id);
  if (!c) return id.slice(0, 8);
  return c.kind === 'git_key' ? t.repos.list.deployKey(c.name) : c.name;
}
