import { createRootRoute, createRoute, createRouter, redirect } from '@tanstack/react-router';
import { RootRedirect } from './routes/RootRedirect.tsx';
import { OrgLayout } from './routes/OrgLayout.tsx';
import { AccountSecurityLayout } from './routes/AccountSecurityLayout.tsx';
import { LoginPage } from './features/auth/LoginPage.tsx';
import { SetupWizardPage } from './features/setup/SetupWizardPage.tsx';
import { InviteAcceptPage } from './features/members/InviteAcceptPage.tsx';
import { HostsListPage } from './features/hosts/HostsListPage.tsx';
import { HostDetailPage } from './features/hosts/HostDetailPage.tsx';
import { MembersPage } from './features/members/MembersPage.tsx';
import { ActivityPage } from './features/activity/ActivityPage.tsx';
import { TokensPage } from './features/tokens/TokensPage.tsx';

/**
 * Code-based routing (createRootRoute/createRoute/createRouter), not
 * TanStack Router's file-based generator: file-based routing needs its
 * Vite plugin to run (even just `vite dev`/`vite build`) to (re)write
 * routeTree.gen.ts, which would make `vitest` and `tsc -b` depend on a
 * generation step no CI job here runs on its own. Every route from the
 * task brief's Interfaces section is declared explicitly below instead.
 */
const rootRoute = createRootRoute();

const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });

const setupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/setup', component: SetupWizardPage });

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (search: Record<string, unknown>) => ({
    step: typeof search.step === 'string' ? search.step : undefined,
  }),
  component: LoginPage,
});

const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invite/$token',
  component: InviteAcceptPage,
});

const accountSecurityRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/account/security',
  component: AccountSecurityLayout,
});

const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org', component: OrgLayout });

const orgIndexRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/o/$org/hosts', params: { org: params.org } });
  },
});

const hostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/hosts', component: HostsListPage });
const hostDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/hosts/$id',
  component: HostDetailPage,
});
const membersRoute = createRoute({ getParentRoute: () => orgRoute, path: '/members', component: MembersPage });
const activityRoute = createRoute({ getParentRoute: () => orgRoute, path: '/activity', component: ActivityPage });
const tokensRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/settings/tokens',
  component: TokensPage,
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  setupRoute,
  loginRoute,
  inviteRoute,
  accountSecurityRoute,
  orgRoute.addChildren([orgIndexRoute, hostsRoute, hostDetailRoute, membersRoute, activityRoute, tokensRoute]),
]);

export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
