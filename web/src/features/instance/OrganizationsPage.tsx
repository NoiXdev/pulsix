import { useState } from 'react';
import { Plus } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Card, GridHeader } from '../../ui/table.tsx';
import { ListRow } from '../../ui/list-row.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useAdminOrgs } from '../../api/hooks/useOrgs.ts';
import { CreateOrgDialog } from '../orgs/CreateOrgDialog.tsx';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 1.6fr) 120px 140px';

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
 * The count includes deactivated accounts, which the hint says.
 */
export function OrganizationsPage() {
  const t = useT();
  const fmt = useFormat();
  const o = t.instance.orgs;
  const orgs = useAdminOrgs();
  const [creating, setCreating] = useState(false);

  const guard = useQueryGuard(orgs);
  if (guard) return guard;

  return (
    <>
      <div className="flex items-center justify-between gap-3">
        <h1 className="text-xl font-semibold">{t.settings.tabs.organizations}</h1>
        <Button variant="primary" onClick={() => setCreating(true)}>
          <Plus size={14} /> {o.create.open}
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">{o.hint}</p>
      {!orgs.data ? (
        <QueryLoading />
      ) : (
        <Card>
          <GridHeader columns={COLUMNS}>
            <span>{o.columns.organization}</span>
            <span>{o.columns.members}</span>
            <span>{o.columns.created}</span>
          </GridHeader>
          {orgs.data.map((org) => {
            const cells = (
              <>
                <div className="flex min-w-0 flex-col">
                  <span className="flex min-w-0 items-center gap-2">
                    <span className="truncate text-[13px] font-semibold">{org.name}</span>
                    {!org.actor_is_member && <Pill tone="unknown">{o.notMember}</Pill>}
                  </span>
                  <span className="truncate font-mono text-xs text-muted-foreground">{org.slug}</span>
                </div>
                <span className="font-mono text-xs">{org.member_count}</span>
                <span className="text-xs text-muted-foreground">{fmt.date(org.created_at)}</span>
              </>
            );
            return org.actor_is_member ? (
              <ListRow
                key={org.id}
                to="/o/$org/hosts"
                params={{ org: org.slug }}
                search={{ q: undefined }}
                columns={COLUMNS}
                label={org.name}
              >
                {cells}
              </ListRow>
            ) : (
              <div
                key={org.id}
                title={o.notMemberTitle}
                className="grid items-center gap-3 border-b border-border px-4 py-2.5 last:border-b-0"
                style={{ gridTemplateColumns: COLUMNS }}
              >
                {cells}
              </div>
            );
          })}
          {orgs.data.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{o.empty}</p>}
        </Card>
      )}
      <CreateOrgDialog open={creating} onClose={() => setCreating(false)} />
    </>
  );
}
