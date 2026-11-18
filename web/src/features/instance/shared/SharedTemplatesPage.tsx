import { useParams } from '@tanstack/react-router';
import { SharedListFrame } from './SharedListFrame.tsx';
import { useSharedRowActions } from './useSharedRowActions.ts';
import { sharedTemplateColumns } from './sharedTemplateColumns.tsx';
import { useListView } from '../../lists/useListView.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { DataList } from '../../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../../ui/list-toolbar.tsx';
import { useListFilters } from '../../lists/filterSearch.ts';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useRepoDefinitions } from '../../../api/hooks/useSharedRepoDefinitions.ts';
import { useDeleteTemplateDefinition, useTemplateDefinitions } from '../../../api/hooks/useSharedTemplateDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { TemplateDefinition } from '../../../api/types.ts';
import { SHARED_FILTERS, filterSharedTemplates } from './sharedSearch.ts';

/**
 * SharedTemplatesPage lists the installation's job template definitions
 * (SharedReposPage's own shape, same gate, same delete flow). A definition's
 * shared half (spec decision 2) is the name, the shared repository its
 * playbook resolves against, the playbook's path, the limit and the extra
 * vars; the scope and the SSH credential stay each organization's own.
 *
 * List id `shared-templates`: one request, sorted and paged in the browser.
 *
 * A delete a schedule or job trigger still references refuses with 409
 * naming the organization (job_template_vault_credentials cascade on
 * purpose and never appear in that message).
 */
export function SharedTemplatesPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const definitions = useTemplateDefinitions();
  const repoDefinitions = useRepoDefinitions();
  const remove = useDeleteTemplateDefinition();
  const rowActions = useSharedRowActions<TemplateDefinition>({
    org,
    to: '/o/$org/settings/shared/templates/$id',
    remove: remove.mutateAsync,
    confirmMessage: (d) => t.instance.shared.templates.confirmDelete(d.name),
    deletedMessage: (d) => t.instance.shared.templates.deleted(d.name),
  });
  const repoName = new Map((repoDefinitions.data?.definitions ?? []).map((d) => [d.id, d.name]));
  const columns = sharedTemplateColumns(t, repoName);
  const { filters, active, setFilter, reset } = useListFilters(SHARED_FILTERS);
  const view = useListView('shared-templates', columns, { mode: 'client', rowKey: (d) => d.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const all = definitions.data.definitions ?? [];
  const shown = filterSharedTemplates(all, filters, repoName);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const s = t.instance.shared.templates;

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedTemplates}
      hint={t.instance.shared.templates.listHint}
      addLabel={t.instance.shared.templates.add}
      addTo="/o/$org/settings/shared/templates/new"
      org={org}
    >
      <DataList
        label={t.instance.shared.templates.label}
        columns={columns}
        rows={rows}
        rowKey={(d) => d.id}
        rowLink={(d) => ({ to: '/o/$org/settings/shared/templates/$id', params: { org, id: d.id }, label: d.name })}
        rowActions={rowActions}
        loading={!view.ready}
        empty={active ? s.noMatch : s.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={all.length} noun={s.count} />}>
            <SearchInput label={s.search} placeholder={s.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, s.count)}
      />
    </SharedListFrame>
  );
}
