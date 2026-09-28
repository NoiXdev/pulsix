import { Link } from '@tanstack/react-router';

const ACTIVE = 'border-b-2 border-primary py-2.5 text-[13px] font-semibold text-foreground';
const INACTIVE = 'border-b-2 border-transparent py-2.5 text-[13px] font-medium text-muted-foreground';

/**
 * ChecksTabs is the strip both pages of the Checks section carry, the same
 * shape features/settings/SettingsTabs.tsx gives the Settings section: real router links,
 * so the URL, the breadcrumbs and the back button stay in sync with which
 * page is open.
 *
 * Monitoring groups are not a peer of Hosts or Runs — they exist to bind
 * check definitions to a set of hosts at once — so they live under Checks
 * as a sibling page rather than as a twelfth entry in the sidebar. Both
 * pages need the same permission (checks.read), so the strip itself has
 * nothing to gate.
 */
export function ChecksTabs({ org }: { org: string }) {
  return (
    <div role="tablist" className="flex gap-6 border-b border-border">
      <Link
        to="/o/$org/checks"
        params={{ org }}
        role="tab"
        activeOptions={{ exact: true }}
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        Checks
      </Link>
      <Link
        to="/o/$org/checks/monitoring-groups"
        params={{ org }}
        role="tab"
        activeProps={{ className: ACTIVE, 'aria-selected': true }}
        inactiveProps={{ className: INACTIVE, 'aria-selected': false }}
      >
        Monitoring groups
      </Link>
    </div>
  );
}
