import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { ListPlus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useGroups } from '../../api/hooks/useInventory.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { FilterSelect } from '../lists/FilterSelect.tsx';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { templateColumns } from './templateColumns.tsx';
import { TEMPLATE_SCOPES, TEMPLATES_FILTERS, filterTemplates } from './templatesSearch.ts';
import { useTemplateListActions } from './useTemplateListActions.tsx';
import { TemplateDialog } from './TemplateDialog.tsx';

/** TemplatesPage lists the job templates (spec §7, backlog 2.10): a
 * playbook plus scope, limit, extra vars and the default SSH credential —
 * the thing an operator runs, and the run view's own entry point. The list (id
 * `templates`) is one request, sorted and paged in the browser; Run, Variables,
 * Triggers, Schedules, Edit and Delete are the row menu's. `?create=<playbook>`
 * opens the new template dialog with that playbook chosen, once the
 * playbooks are known; an id that is none of them (or no UUID at all)
 * opens it without a playbook. */
export function TemplatesPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { create?: string };
  const orgInfo = useOrg(org);
  const templates = useTemplates(org);
  const playbooks = usePlaybooks(org);
  const groups = useGroups(org);
  const canWrite = may(orgInfo.data?.org.role, 'templates.write');
  const canRun = may(orgInfo.data?.org.role, 'jobs.run');
  const canReadCredentials = may(orgInfo.data?.org.role, 'credentials.read');
  // Gates the Default SSH credential picker's own create row:
  // credentials.write is admin, stricter than templates.write (v0.4g
  // Task 7 fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const actions = useTemplateListActions(org, { canWrite, canRun, canReadCredentials, canCreateCredential });

  const credentialName = (id: string) => (credentials.data?.credentials ?? []).find((c) => c.id === id)?.name;
  const groupName = (id: string) => (groups.data?.groups ?? []).find((g) => g.id === id)?.name ?? id.slice(0, 8);
  const playbookLabel = (id: string) => {
    // "" is the unresolved-projection case (toTemplateDTO's own comment,
    // internal/api/template_handlers.go): this organization has not synced
    // a matching playbook from the shared repository yet. Distinguished
    // from "a real id this page's own playbook list does not know about"
    // (id.slice(0, 8)) rather than falling through to the same fallback,
    // which would otherwise render as an empty string.
    if (id === '') return t.templates.playbook.notSynced;
    const p = (playbooks.data?.playbooks ?? []).find((x) => x.id === id);
    if (!p) return id.slice(0, 8);
    return p.source === 'repo' ? t.templates.playbook.repo(p.path ?? '') : p.summary || t.templates.playbook.editor;
  };

  const columns = templateColumns({ t, playbookLabel, groupName, credentialName });
  const { filters, active, setFilter, reset } = useListFilters(TEMPLATES_FILTERS);
  const view = useListView('templates', columns, { mode: 'client', rowKey: (tpl) => tpl.id, resetKey: JSON.stringify(filters) });

  // The preset waits for the playbook list: the form takes it once, on mount.
  const createReady = !!search.create && (!!playbooks.data || playbooks.isError);
  const preset = presetPlaybook(search.create, playbooks.data?.playbooks);

  const guard = useQueryGuard(templates);
  if (guard) return guard;
  if (!templates.data) return <QueryLoading />;
  const list = templates.data.templates ?? [];
  const shown = filterTemplates(list, filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);
  const f = t.templates.list.filters;
  // Only playbooks a template runs are offered; one only the URL names stays so it can be cleared.
  const playbookIds = [...new Set([...list.map((tpl) => tpl.playbook_id).filter(Boolean), ...(filters.playbook ? [filters.playbook] : [])])];
  const playbookOptions = playbookIds.map((id) => ({ value: id, label: playbookLabel(id) })).sort((a, b) => a.label.localeCompare(b.label));
  const scopeLabels = { whole: t.templates.scope.whole, group: f.scopeGroup, hosts: f.scopeHosts };

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.templates.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.templates.list.summary(list.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={actions.openNew}>
            <ListPlus size={15} /> {t.templates.list.add}
          </Button>
        )}
      </div>
      <DataList
        label={t.templates.list.label}
        columns={columns}
        rows={rows}
        rowKey={(tpl) => tpl.id}
        rowLabel={(tpl) => tpl.name}
        rowActions={actions.rowActions}
        onRowActivate={actions.edit}
        loading={!view.ready}
        empty={active ? t.templates.list.noMatch : t.templates.list.empty}
        toolbar={
          <ListToolbar count={<ResultCount active={active} shown={shown.length} total={list.length} noun={t.templates.list.count} />}>
            <SearchInput
              label={t.templates.list.search}
              placeholder={t.templates.list.filterPlaceholder}
              value={filters.q ?? ''}
              onChange={(q) => setFilter('q', q || undefined, { replace: true })}
            />
            <FilterSelect label={f.playbook} allLabel={f.allPlaybooks} options={playbookOptions} value={filters.playbook} onChange={(v) => setFilter('playbook', v)} />
            <FilterSelect
              label={f.scope}
              allLabel={f.anyScope}
              options={TEMPLATE_SCOPES.map((v) => ({ value: v, label: scopeLabels[v] }))}
              value={filters.scope}
              onChange={(v) => setFilter('scope', v as typeof filters.scope)}
            />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, t.templates.list.count)}
      />
      {actions.dialogs}
      {canWrite && createReady && (
        <TemplateDialog
          org={org}
          canReadCredentials={canReadCredentials}
          canCreateCredential={canCreateCredential}
          initialPlaybookId={preset}
          onClose={() => void navigate({ to: '/o/$org/templates', params: { org }, search: (prev) => ({ ...prev, create: undefined }), replace: true })}
        />
      )}
    </>
  );
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The `?create=` playbook when it is one of the organization's, else none. */
function presetPlaybook(id: string | undefined, playbooks: readonly { id: string }[] | null | undefined): string | undefined {
  if (!id || !UUID.test(id)) return undefined;
  return playbooks?.some((p) => p.id === id) ? id : undefined;
}
