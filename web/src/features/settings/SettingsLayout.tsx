import { Outlet } from '@tanstack/react-router';

/**
 * SettingsLayout frames the Settings section. Its navigation (Organization,
 * Alerting, Instance, beside My account) is the shell's own sidebar, which
 * OrgLayout swaps in under /o/$org/settings, so only the page renders here.
 */
export function SettingsLayout() {
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <Outlet />
    </div>
  );
}
