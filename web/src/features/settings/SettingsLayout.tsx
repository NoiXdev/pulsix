import { Outlet, useParams } from '@tanstack/react-router';
import { useMe } from '../../api/hooks/useMe.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { SideNav, type SideNavGroup } from '../../ui/side-nav.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * SettingsLayout is the frame of the Settings section: a grouped side
 * navigation on the left, the page on the right. Real router links, so the
 * URL, the breadcrumbs and the back button stay in sync with the open page.
 *
 * It lives under features, not in src/ui, because deciding which items a
 * caller may see means asking who is signed in — src/ui is presentational
 * and fetches nothing.
 *
 * Which group a role sees:
 * - Organization (General, Members, Activity, API tokens): every member.
 * - Alerting (Notifications, Alert routes, On-call rotations): the roles
 *   that may read channels (channels.read, operator and above).
 *   alert_route_handlers.go and oncall_handlers.go gate their own reads on
 *   that same permission, since either decides where alert data leaves the
 *   system, the same weight a channel itself carries.
 * - Instance (Users, Outgoing mail, Language, Backups, Shared repositories,
 *   Shared job templates, Shared check definitions): instance administrators
 *   only, the only callers GET /admin/users, /admin/settings/smtp,
 *   /admin/settings/locale, /admin/backups and
 *   /admin/{repos,templates,checks}/definitions admit.
 *
 * Credentials and Jump hosts are sidebar entries under Infrastructure, not
 * Settings items (v0.4f).
 */
export function SettingsLayout() {
  const { org } = useParams({ strict: false }) as { org: string };
  const me = useMe({ enabled: true });
  const orgInfo = useOrg(org);
  const t = useT();
  const params = { org };
  const role = orgInfo.data?.org.role;

  const groups: SideNavGroup[] = [
    {
      label: t.settings.nav.organization,
      items: [
        { to: '/o/$org/settings/general', params, label: t.settings.tabs.general },
        { to: '/o/$org/settings/members', params, label: t.settings.tabs.members },
        { to: '/o/$org/settings/activity', params, label: t.settings.tabs.activity },
        { to: '/o/$org/settings/tokens', params, label: t.settings.tabs.tokens },
      ],
    },
  ];
  if (may(role, 'channels.read')) {
    groups.push({
      label: t.settings.nav.alerting,
      items: [
        { to: '/o/$org/settings/notifications', params, label: t.settings.tabs.notifications },
        { to: '/o/$org/settings/alert-routes', params, label: t.settings.tabs.alertRoutes },
        { to: '/o/$org/settings/rotations', params, label: t.settings.tabs.rotations },
      ],
    });
  }
  if (me.data?.instance_admin) {
    groups.push({
      label: t.settings.nav.instance,
      items: [
        { to: '/o/$org/settings/users', params, label: t.settings.tabs.users },
        { to: '/o/$org/settings/mail', params, label: t.settings.tabs.mail },
        { to: '/o/$org/settings/language', params, label: t.language.instance.tab },
        { to: '/o/$org/settings/backups', params, label: t.backups.tab },
        { to: '/o/$org/settings/shared/repos', params, label: t.settings.tabs.sharedRepos },
        { to: '/o/$org/settings/shared/templates', params, label: t.settings.tabs.sharedTemplates },
        { to: '/o/$org/settings/shared/checks', params, label: t.settings.tabs.sharedChecks },
      ],
    });
  }

  return (
    <div className="grid grid-cols-1 items-start gap-6 min-[900px]:grid-cols-[220px_minmax(0,1fr)]">
      <div className="min-w-0 min-[900px]:sticky min-[900px]:top-6 min-[900px]:self-start">
        <SideNav groups={groups} label={t.settings.nav.label} />
      </div>
      <div className="flex min-w-0 flex-col gap-5">
        <Outlet />
      </div>
    </div>
  );
}
