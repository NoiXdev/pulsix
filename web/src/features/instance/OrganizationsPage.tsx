import { useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList } from '../../ui/data-list/index.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminOrgs } from '../../api/hooks/useOrgs.ts';
import { CreateOrgDialog } from '../orgs/CreateOrgDialog.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { organizationColumns } from './organizationColumns.tsx';

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
  const view = useListView('organizations', columns, { mode: 'client', rowKey: (x) => x.id });

  const guard = useQueryGuard(orgs);
  if (guard) return guard;
  const { rows, total } = view.view(view.ready ? orgs.data : undefined);

  return (
    <>
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">{t.settings.tabs.organizations}</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <Plus size={14} /> {o.create.open}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{o.hint}</p>
      <DataList
        label={o.label}
        columns={columns}
        rows={rows}
        rowKey={(x) => x.id}
        rowLink={(x) =>
          x.actor_is_member ? { to: '/o/$org/hosts', params: { org: x.slug }, search: { q: undefined }, label: x.name } : undefined
        }
        loading={!orgs.data || !view.ready}
        empty={o.empty}
        {...view.listProps(total, o.count)}
      />
      <CreateOrgDialog open={creating} onClose={() => setCreating(false)} />
    </>
  );
}
