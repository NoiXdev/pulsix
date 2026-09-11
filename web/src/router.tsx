import { createRootRoute, createRoute, createRouter, redirect } from '@tanstack/react-router';
import { RootRedirect } from './routes/RootRedirect.tsx';
import { OrgLayout } from './routes/OrgLayout.tsx';
import { AccountSecurityLayout } from './routes/AccountSecurityLayout.tsx';
import { EnrolmentLayout } from './routes/EnrolmentLayout.tsx';
import { LoginPage } from './features/auth/LoginPage.tsx';
import { SetupWizardPage } from './features/setup/SetupWizardPage.tsx';
import { InviteAcceptPage } from './features/members/InviteAcceptPage.tsx';
import { HostsListPage } from './features/hosts/HostsListPage.tsx';
import { HostDetailPage } from './features/hosts/HostDetailPage.tsx';
import { GroupsPage } from './features/inventory/GroupsPage.tsx';
import { GroupDetailPage } from './features/inventory/GroupDetailPage.tsx';
import { MembersPage } from './features/members/MembersPage.tsx';
import { ActivityPage } from './features/activity/ActivityPage.tsx';
import { TokensPage } from './features/tokens/TokensPage.tsx';
import { SmtpSettingsPage } from './features/instance/SmtpSettingsPage.tsx';
import { CredentialsPage } from './features/credentials/CredentialsPage.tsx';
import { JumpHostsPage } from './features/credentials/JumpHostsPage.tsx';

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

// /account/enrol is where a session whose account has no second factor yet
// is sent (a second factor is mandatory): it renders without the app shell,
// since such a session cannot load organizations.
const enrolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/account/enrol',
  component: EnrolmentLayout,
});

const orgRoute = createRoute({ getParentRoute: () => rootRoute, path: '/o/$org', component: OrgLayout });

const orgIndexRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/o/$org/hosts', params: { org: params.org }, search: { q: undefined } });
  },
});

const hostsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/hosts',
  // ⌘K search (ui/shell.tsx) navigates here with ?q=..., wired to the
  // /orgs/{org}/hosts?q= filter the API already supports (finding
  // "Important 6": the search box used to be a static span).
  validateSearch: (search: Record<string, unknown>) => ({
    q: typeof search.q === 'string' ? search.q : undefined,
  }),
  component: HostsListPage,
});
const hostDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/hosts/$id',
  component: HostDetailPage,
});

const groupsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/groups', component: GroupsPage });
const groupDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/groups/$id',
  validateSearch: (search: Record<string, unknown>) => ({
    tab: typeof search.tab === 'string' ? search.tab : undefined,
  }),
  component: GroupDetailPage,
});

/**
 * settingsRoute is the layout route for Members, Activity and API tokens —
 * finding "Important 6": the mockup (Members.dc.html, Activity.dc.html)
 * files all three under one "Settings" section (gear icon, single nav
 * entry) with breadcrumbs like "Settings › Members", not as separate
 * top-level nav items. Nesting them under one real route both matches
 * that IA and lets the shell's Settings nav link mark itself active via
 * the router's own activeProps (matching any descendant of /settings)
 * instead of hand-rolled pathname string matching.
 */
const settingsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/settings' });
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/o/$org/settings/members', params: { org: params.org } });
  },
});
const membersRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/members', component: MembersPage });
const activityRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/activity',
  component: ActivityPage,
});
const tokensRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/tokens', component: TokensPage });
const credentialsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/credentials', component: CredentialsPage });
const jumpHostsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/jump-hosts', component: JumpHostsPage });
// Instance-wide outgoing mail settings, for instance administrators; filed
// under Settings for navigation only (see SmtpSettingsPage).
const mailRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/mail', component: SmtpSettingsPage });

const routeTree = rootRoute.addChildren([
  indexRoute,
  setupRoute,
  loginRoute,
  inviteRoute,
  accountSecurityRoute,
  enrolRoute,
  orgRoute.addChildren([
    orgIndexRoute,
    hostsRoute,
    hostDetailRoute,
    groupsRoute,
    groupDetailRoute,
    settingsRoute.addChildren([settingsIndexRoute, membersRoute, activityRoute, tokensRoute, credentialsRoute, jumpHostsRoute, mailRoute]),
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
