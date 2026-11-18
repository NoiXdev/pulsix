import { SettingsCard, SettingsPage, SettingsRow } from '../../ui/settings.tsx';
import { Pill } from '../../ui/badge.tsx';
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
    <SettingsPage title={t.profile.title} description={t.profile.subtitle}>
      <SettingsCard title={t.profile.cardTitle} description={t.profile.adminNote}>
        <SettingsRow label={t.profile.name}>
          <span className="font-medium">{me.data.user.display_name}</span>
        </SettingsRow>
        <SettingsRow label={t.profile.email}>
          <span className="font-mono">{me.data.user.email}</span>
        </SettingsRow>
      </SettingsCard>
      <SettingsCard title={t.profile.organizations} description={t.profile.organizationsDescription}>
        {list.length === 0 ? (
          <p className="border-t border-border px-5 py-3.5 text-[12.5px] text-muted-foreground">{t.profile.noOrganizations}</p>
        ) : (
          <ul>
            {list.map((o) => (
              <li key={o.id}>
                <SettingsRow label={o.name}>
                  <Pill tone="accent">{roleLabel(t, o.role)}</Pill>
                </SettingsRow>
              </li>
            ))}
          </ul>
        )}
      </SettingsCard>
    </SettingsPage>
  );
}
