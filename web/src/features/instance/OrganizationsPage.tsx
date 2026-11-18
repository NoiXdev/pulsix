import { useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { ListToolbar, ResetFilters, ResultCount, SearchInput } from '../../ui/list-toolbar.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminOrgs } from '../../api/hooks/useOrgs.ts';
import { CreateOrgDialog } from '../orgs/CreateOrgDialog.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListFilters } from '../lists/filterSearch.ts';
import { useListView } from '../lists/useListView.ts';
import { ORGANIZATIONS_FILTERS, filterOrganizations } from './organizationsSearch.ts';
import { organizationColumns } from './organizationColumns.tsx';
import { PageHeader } from '../../ui/settings.tsx';

/**
 * OrganizationsPage lists every organization on the instance for its
 * administrators, with the member count and creation date, and offers the
 * same create dialog as the org switcher. GET /admin/orgs admits instance
 * admins only, so anyone else who reaches the URL sees the access-denied
 * page.
 *
 * An organization the admin belongs to opens its hosts page. Any other is
 * listed but cannot be opened: an instance admin is not made a member by
 * looking at an organization (backlog 7.74 adds no "join as admin").
 * The count is of active accounts: deactivated members are not counted.
 * List id `organizations`: one request, sorted and paged in the browser.
 */
export function OrganizationsPage() {
  const t = useT();
  const fmt = useFormat();
  const o = t.instance.orgs;
  const orgs = useAdminOrgs();
  const [creating, setCreating] = useState(false);
  const columns = organizationColumns(t, fmt);
  const { filters, active, setFilter, reset } = useListFilters(ORGANIZATIONS_FILTERS);
  const view = useListView('organizations', columns, { mode: 'client', rowKey: (x) => x.id, resetKey: JSON.stringify(filters) });

  const guard = useQueryGuard(orgs);
  if (guard) return guard;
  const shown = filterOrganizations(orgs.data ?? [], filters);
  const { rows, total } = view.view(view.ready ? shown : undefined);

  return (
    <>
      <PageHeader
        title={t.settings.tabs.organizations}
        description={o.hint}
        actions={
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Plus size={14} /> {o.create.open}
          </Button>
        }
      />
      <DataList
        label={o.label}
        columns={columns}
        rows={rows}
        rowKey={(x) => x.id}
        rowLink={(x) =>
          x.actor_is_member ? { to: '/o/$org/hosts', params: { org: x.slug }, search: { q: undefined }, label: x.name } : undefined
        }
        loading={!orgs.data || !view.ready}
        empty={active ? o.noMatch : o.empty}
        toolbar={
          <ListToolbar count={active ? <ResultCount shown={shown.length} total={orgs.data?.length} noun={o.count} /> : undefined}>
            <SearchInput label={o.search} placeholder={o.filterPlaceholder} value={filters.q ?? ''} onChange={(q) => setFilter('q', q || undefined, { replace: true })} />
            {active && <ResetFilters onReset={reset} />}
          </ListToolbar>
        }
        {...view.listProps(total, o.count)}
      />
      <CreateOrgDialog open={creating} onClose={() => setCreating(false)} />
    </>
  );
}
