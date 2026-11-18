import { useParams } from '@tanstack/react-router';
import { SharedListFrame } from './SharedListFrame.tsx';
import { useSharedRowActions } from './useSharedRowActions.ts';
import { sharedCheckColumns } from './sharedCheckColumns.tsx';
import { useListView } from '../../lists/useListView.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { DataList } from '../../../ui/data-list/index.ts';
import { FilterSelect } from '../../lists/FilterSelect.tsx';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../../ui/list-toolbar.tsx';
import { useListFilters } from '../../lists/filterSearch.ts';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useCheckDefinitions, useDeleteCheckDefinition } from '../../../api/hooks/useSharedCheckDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { CheckDefinitionDef } from '../../../api/types.ts';
import { SHARED_CHECKS_FILTERS, filterSharedChecks } from './sharedSearch.ts';

/**
 * SharedChecksPage lists the installation's check definitions
 * (SharedReposPage's own shape, same gate, same delete flow). A definition's
 * shared half (spec decision 2) is the key, plugin, params and interval; the
 * binding and the enabled flag belong to the projection, which each
 * organization edits through its own Checks page.
 *
 * List id `shared-checks`: one request, sorted and paged in the browser.
 *
 * Unlike the other two kinds, a check definition's delete never refuses with
 * 409: nothing in the schema references check_definitions (internal/shared's
 * DeleteCheckDefinitionWithProjections), so the confirmation says what the
 * delete removes rather than warning about a dependant that cannot exist.
 */
export function SharedChecksPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const definitions = useCheckDefinitions();
  const remove = useDeleteCheckDefinition();
  const rowActions = useSharedRowActions<CheckDefinitionDef>({
    org,
    to: '/o/$org/settings/shared/checks/$id',
    remove: remove.mutateAsync,
    confirmMessage: (d) => t.instance.shared.checks.confirmDelete(d.key),
    deletedMessage: (d) => t.instance.shared.checks.deleted(d.key),
  });
  const columns = sharedCheckColumns(t);
  const { filters, active, setFilter, reset } = useListFilters(SHARED_CHECKS_FILTERS);
  const view = useListView('shared-checks', columns, { mode: 'client', rowKey: (d) => d.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const all = definitions.data.definitions ?? [];
  const shown = filterSharedChecks(all, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const s = t.instance.shared.checks;
  // A plugin only the URL names (a stale link) stays pickable so it can be cleared.
  const plugins = [...new Set([...all.map((d) => d.plugin), ...(filters.plugin ? [filters.plugin] : [])])].sort();

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedChecks}
      hint={t.instance.shared.checks.listHint}
      addLabel={t.instance.shared.checks.add}
      addTo="/o/$org/settings/shared/checks/new"
      org={org}
    >
      <DataList
        label={t.instance.shared.checks.label}
        columns={columns}
        rows={rows}
        rowKey={(d) => d.id}
        rowLink={(d) => ({ to: '/o/$org/settings/shared/checks/$id', params: { org, id: d.id }, label: d.key })}
        rowActions={rowActions}
        loading={!view.ready}
        empty={active ? s.noMatch : s.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={all.length} noun={s.count} />}>
            <SearchInput label={s.search} placeholder={s.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            <FilterSelect label={s.filterPlugin} allLabel={s.allPlugins} options={plugins.map((p) => ({ value: p, label: p }))} value={filters.plugin} onChange={(v) => setFilter('plugin', v)} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, s.count)}
      />
    </SharedListFrame>
  );
}
