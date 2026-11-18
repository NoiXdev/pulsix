import { useParams } from '@tanstack/react-router';
import { SharedListFrame } from './SharedListFrame.tsx';
import { useSharedRowActions } from './useSharedRowActions.ts';
import { sharedRepoColumns } from './sharedRepoColumns.tsx';
import { useListView } from '../../lists/useListView.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { DataList } from '../../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../../ui/list-toolbar.tsx';
import { useListFilters } from '../../lists/filterSearch.ts';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useDeleteRepoDefinition, useRepoDefinitions } from '../../../api/hooks/useSharedRepoDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { RepoDefinition } from '../../../api/types.ts';
import { SHARED_FILTERS, filterSharedRepos } from './sharedSearch.ts';

/**
 * SharedReposPage lists the installation's repository definitions: the
 * provider's own repositories, defined once for every organization (spec
 * v0.2f, decisions 1-8) and projected into each as an ordinary, read-only
 * row — each organization still supplies its own credential, which never
 * belongs to a definition (decision 3).
 *
 * Reachable with Principal.InstanceAdmin only: GET /admin/repos/definitions
 * admits no other caller. The nav item is hidden from everyone else and the
 * route itself is not guarded client-side, so anyone else who reaches it —
 * this list, or the form pages, which load the same list first
 * (DefinitionLoader) — sees the 403 page rather than a screen that could
 * only fail. It lives under an organization's Settings for navigation only —
 * the definitions are instance-wide.
 *
 * A row opens SharedRepoFormPage; Delete… confirms and then deletes
 * (useSharedRowActions, which also shows a refused delete's own message).
 * List id `shared-repos`: one request, sorted and paged in the browser.
 */
export function SharedReposPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const definitions = useRepoDefinitions();
  const remove = useDeleteRepoDefinition();
  const rowActions = useSharedRowActions<RepoDefinition>({
    org,
    to: '/o/$org/settings/shared/repos/$id',
    remove: remove.mutateAsync,
    confirmMessage: (d) => t.instance.shared.repos.confirmDelete(d.name),
    deletedMessage: (d) => t.instance.shared.repos.deleted(d.name),
  });
  const columns = sharedRepoColumns(t);
  const { filters, active, setFilter, reset } = useListFilters(SHARED_FILTERS);
  const view = useListView('shared-repos', columns, { mode: 'client', rowKey: (d) => d.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const all = definitions.data.definitions ?? [];
  const shown = filterSharedRepos(all, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const s = t.instance.shared.repos;

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedRepos}
      hint={t.instance.shared.repos.listHint}
      addLabel={t.instance.shared.repos.add}
      addTo="/o/$org/settings/shared/repos/new"
      org={org}
    >
      <DataList
        label={t.instance.shared.repos.label}
        columns={columns}
        rows={rows}
        rowKey={(d) => d.id}
        rowLink={(d) => ({ to: '/o/$org/settings/shared/repos/$id', params: { org, id: d.id }, label: d.name })}
        rowActions={rowActions}
        loading={!view.ready}
        empty={active ? s.noMatch : s.empty}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={all.length} noun={s.count} /> : undefined}>
            <SearchInput label={s.search} placeholder={s.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, s.count)}
      />
    </SharedListFrame>
  );
}
