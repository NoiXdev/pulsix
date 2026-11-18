import { useOrgs } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';

/**
 * useSettingsOrgs returns the slugs among `slugs` whose Settings → General
 * the viewer may change (the same check the page makes for assistant
 * access), so the connect guide links to it only where it will open.
 */
export function useSettingsOrgs(slugs: readonly string[]): string[] {
  const orgs = useOrgs();
  return (orgs.data?.orgs ?? [])
    .filter((o) => slugs.includes(o.slug) && may(o.role, 'org.settings.write'))
    .map((o) => o.slug);
}
