import { Link } from '@tanstack/react-router';
import { useMe } from '../api/hooks/useMe.ts';

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
 * "Outgoing mail" is instance-wide and shown only to instance
 * administrators — the only callers GET /admin/settings/smtp admits.
 */
export function SettingsTabs({ org }: { org: string }) {
  const me = useMe({ enabled: true });
  return (
    <div role="tablist" className="flex gap-6 border-b border-border">
      <Link
        to="/o/$org/settings/members"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        Members
      </Link>
      <Link
        to="/o/$org/settings/activity"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        Activity
      </Link>
      <Link
        to="/o/$org/settings/tokens"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        API tokens
      </Link>
      {me.data?.instance_admin && (
        <Link
          to="/o/$org/settings/mail"
          params={{ org }}
          role="tab"
          activeProps={{ className: ACTIVE, 'aria-selected': true }}
          inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
        >
          Outgoing mail
        </Link>
      )}
    </div>
  );
}
