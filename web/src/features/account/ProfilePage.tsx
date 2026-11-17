import { Card } from '../../ui/table.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useMe } from '../../api/hooks/useMe.ts';
import { useOrgs } from '../../api/hooks/useOrgs.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';
import { useT } from '../../i18n/useT.ts';

/** ProfilePage is Settings > Profile: who the person is, read-only. Name
 * and email are changed by an administrator, so there is nothing to edit. */
export function ProfilePage() {
  const t = useT();
  const me = useMe({ enabled: true });
  const orgs = useOrgs();
  const meGuard = useQueryGuard(me);
  const orgsGuard = useQueryGuard(orgs);
  if (meGuard) return meGuard;
  if (orgsGuard) return orgsGuard;
  if (!me.data || !orgs.data) return <QueryLoading />;

  const list = orgs.data.orgs ?? [];
  return (
    <>
      <div className="flex flex-col gap-1">
        <h1 className="text-xl font-semibold">{t.profile.title}</h1>
        <span className="text-[13px] text-muted-foreground">{t.profile.subtitle}</span>
      </div>
      <div className="flex max-w-[760px] flex-col gap-4">
        <Card>
          <dl className="grid grid-cols-[120px_minmax(0,1fr)] gap-x-4 gap-y-3 p-4">
            <dt className="text-muted-foreground">{t.profile.name}</dt>
            <dd className="font-medium">{me.data.user.display_name}</dd>
            <dt className="text-muted-foreground">{t.profile.email}</dt>
            <dd className="font-mono">{me.data.user.email}</dd>
          </dl>
          <p className="border-t border-border px-4 py-3 text-[11px] text-muted-foreground">{t.profile.adminNote}</p>
        </Card>
        <Card>
          <div className="flex flex-col gap-2 p-4">
            <h2 className="text-sm font-semibold">{t.profile.organizations}</h2>
            {list.length === 0 ? (
              <p className="text-xs text-muted-foreground">{t.profile.noOrganizations}</p>
            ) : (
              <ul className="flex flex-col divide-y divide-border">
                {list.map((o) => (
                  <li key={o.id} className="flex items-center justify-between gap-3 py-2">
                    <span className="truncate">{o.name}</span>
                    <span className="text-xs text-muted-foreground">{roleLabel(t, o.role)}</span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </Card>
      </div>
    </>
  );
}
