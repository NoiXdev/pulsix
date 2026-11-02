import { Link } from '@tanstack/react-router';
import { useMe } from '../../api/hooks/useMe.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';

const ACTIVE = 'border-b-2 border-primary py-2.5 text-[13px] font-semibold text-foreground';
const INACTIVE = 'border-b-2 border-transparent py-2.5 text-[13px] font-medium text-muted-foreground';

/**
 * SettingsTabs is the tab strip Members.dc.html/Activity.dc.html show at
 * the top of the Settings section (design's `tab()` generator: an
 * underline on the active label). Real router links, not a client-only
 * tab switch, so the URL, breadcrumbs and browser back button all stay in
 * sync with which tab is open — finding "Important 6": Members and
 * Activity moved under one Settings section (task-15a-report.md fix
 * round 1), and this is how a page in that section gets back to its
 * siblings.
 *
 * It lives under features, not in src/ui, because deciding which tabs a
 * role may see means asking who is signed in — src/ui is presentational
 * and fetches nothing (CLAUDE.md, backlog 3.23). features/checks/ChecksTabs
 * is the same strip for the Checks section, without the gating.
 *
 * Credentials and Jump hosts were tabs here until v0.4f; they are
 * sidebar entries under Infrastructure now, and their routes left
 * /settings with them (router.tsx).
 *
 * "Outgoing mail", "Language" and "Shared resources" are instance-wide and
 * shown only to instance administrators — the only callers GET
 * /admin/settings/smtp, GET /admin/settings/locale and GET
 * /admin/repos/definitions admit, respectively (v0.2f, backlog 1.41).
 * "Notifications", "Alert routes" and "On-call rotations" are shown to
 * the roles that may read channels (channels.read, operator and above) —
 * alert_route_handlers.go and oncall_handlers.go both gate their own
 * reads on that same permission, not checks.read, since either decides
 * where alert data leaves the system (the same weight a channel itself
 * carries).
 */
export function SettingsTabs({ org }: { org: string }) {
  const me = useMe({ enabled: true });
  const orgInfo = useOrg(org);
  const t = useT();
  return (
    <div role="tablist" className="flex gap-6 border-b border-border">
      <Link
        to="/o/$org/settings/members"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        {t.settings.tabs.members}
      </Link>
      <Link
        to="/o/$org/settings/activity"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        {t.settings.tabs.activity}
      </Link>
      <Link
        to="/o/$org/settings/tokens"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        {t.settings.tabs.tokens}
      </Link>
      {may(orgInfo.data?.org.role, 'channels.read') && (
        <Link
          to="/o/$org/settings/notifications"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.settings.tabs.notifications}
        </Link>
      )}
      {may(orgInfo.data?.org.role, 'channels.read') && (
        <Link
          to="/o/$org/settings/alert-routes"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.settings.tabs.alertRoutes}
        </Link>
      )}
      {may(orgInfo.data?.org.role, 'channels.read') && (
        <Link
          to="/o/$org/settings/rotations"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.settings.tabs.rotations}
        </Link>
      )}
      {me.data?.instance_admin && (
        <Link
          to="/o/$org/settings/mail"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.settings.tabs.mail}
        </Link>
      )}
      {me.data?.instance_admin && (
        <Link
          to="/o/$org/settings/language"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.language.instance.tab}
        </Link>
      )}
      {me.data?.instance_admin && (
        <Link
          to="/o/$org/settings/shared-resources"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          {t.settings.tabs.sharedResources}
        </Link>
      )}
    </div>
  );
}
