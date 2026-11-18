import { createRootRoute, createRoute, createRouter, redirect } from '@tanstack/react-router';
import { RootLayout } from './routes/RootLayout.tsx';
import { RootRedirect } from './routes/RootRedirect.tsx';
import { OrgLayout } from './routes/OrgLayout.tsx';
import { AccountLayout } from './routes/AccountLayout.tsx';
import { AccountSecurityPage } from './features/account/AccountSecurityPage.tsx';
import { AppearancePage } from './features/account/AppearancePage.tsx';
import { ProfilePage } from './features/account/ProfilePage.tsx';
import { MyActivityPage } from './features/account/MyActivityPage.tsx';
import { PersonalTokensPage } from './features/account/PersonalTokensPage.tsx';
import { validateListSearch } from './features/lists/listSearch.ts';
import { EnrolmentLayout } from './routes/EnrolmentLayout.tsx';
import { LoginPage } from './features/auth/LoginPage.tsx';
import { ForgotPasswordPage } from './features/auth/ForgotPasswordPage.tsx';
import { ResetPasswordPage } from './features/auth/ResetPasswordPage.tsx';
import { ChangePasswordPage } from './features/account/ChangePasswordPage.tsx';
import { LockPinPromptPage } from './features/lock/LockPinPromptPage.tsx';
import { SetupWizardPage } from './features/setup/SetupWizardPage.tsx';
import { InviteAcceptPage } from './features/members/InviteAcceptPage.tsx';
import { HostsListPage } from './features/hosts/HostsListPage.tsx';
import { validateHostsSearch } from './features/hosts/hostsSearch.ts';
import { validateAlertsSearch } from './features/alerts/alertsSearch.ts';
import { validateRunsSearch } from './features/runs/runsSearch.ts';
import { validateHostSearch } from './features/hosts/hostTabs.ts';
import { HostDetailPage } from './features/hosts/HostDetailPage.tsx';
import { FleetPage } from './features/fleet/FleetPage.tsx';
import { AlertsPage } from './features/alerts/AlertsPage.tsx';
import { GroupsPage } from './features/inventory/GroupsPage.tsx';
import { GroupDetailPage } from './features/inventory/GroupDetailPage.tsx';
import { formerSection, validateGroupSearch } from './features/inventory/groupTabs.ts';
import { CredentialsPage } from './features/credentials/CredentialsPage.tsx';
import { JumpHostsPage } from './features/credentials/JumpHostsPage.tsx';
import { ReposPage } from './features/repos/ReposPage.tsx';
import { PlaybooksPage } from './features/playbooks/PlaybooksPage.tsx';
import { TemplatesPage } from './features/templates/TemplatesPage.tsx';
import { TemplateVariablesPage } from './features/templates/variables/TemplateVariablesPage.tsx';
import { SchedulesPage } from './features/schedules/SchedulesPage.tsx';
import { RunsPage } from './features/runs/RunsPage.tsx';
import { RunDetailPage } from './features/runs/RunDetailPage.tsx';
import { RunRequestsPage } from './features/runs/RunRequestsPage.tsx';
import { RunRequestDetailPage } from './features/runs/RunRequestDetailPage.tsx';
import { WorkersPage } from './features/workers/WorkersPage.tsx';
import { AgentsPage } from './features/agents/AgentsPage.tsx';
import { ChecksPage } from './features/checks/ChecksPage.tsx';
import { MonitoringGroupsPage } from './features/checks/MonitoringGroupsPage.tsx';
import { RulesPage } from './features/checks/RulesPage.tsx';
import { ItemsPage } from './features/checks/ItemsPage.tsx';
import { settingsRouteTree } from './routes/settingsRoutes.tsx';
import { MaintenancePage } from './features/maintenance/MaintenancePage.tsx';
import { validateCredentialsSearch } from './features/credentials/credentialsSearch.ts';
import { validateJumpHostsSearch } from './features/credentials/jumpHostsSearch.ts';
import { validateAgentsSearch } from './features/agents/agentsSearch.ts';
import { validateWorkersSearch } from './features/workers/workersSearch.ts';
import { validateFleetSearch } from './features/fleet/fleetSearch.ts';
import { validateChecksSearch } from './features/checks/checksSearch.ts';
import { validateMonitoringGroupsSearch } from './features/checks/monitoringGroupsSearch.ts';
import { validateRulesSearch } from './features/checks/rulesSearch.ts';
import { validateDecisionsSearch } from './features/checks/decisionsSearch.ts';
import { validateReposSearch } from './features/repos/reposSearch.ts';
import { validatePlaybooksSearch } from './features/playbooks/playbooksSearch.ts';
import { validateTemplatesSearch } from './features/templates/templatesSearch.ts';
import { validateSchedulesSearch } from './features/schedules/schedulesSearch.ts';
import { validateRunRequestsSearch } from './features/runs/runRequestsSearch.ts';
import { validateMyActivitySearch } from './features/activity/activitySearch.ts';
import { validateMaintenanceSearch } from './features/maintenance/maintenanceSearch.ts';
import { validateGroupsSearch } from './features/inventory/groupsSearch.ts';

/**
 * Code-based routing (createRootRoute/createRoute/createRouter), not
 * TanStack Router's file-based generator: file-based routing needs its
 * Vite plugin to run (even just `vite dev`/`vite build`) to (re)write
 * routeTree.gen.ts, which would make `vitest` and `tsc -b` depend on a
 * generation step no CI job here runs on its own. Every route from the
 * task brief's Interfaces section is declared explicitly below instead.
 */
// RootLayout takes a session that must change its password to
// /account/password from wherever a request is refused for it.
const rootRoute = createRootRoute({ component: RootLayout });

const indexRoute = createRoute({ getParentRoute: () => rootRoute, path: '/', component: RootRedirect });

const setupRoute = createRoute({ getParentRoute: () => rootRoute, path: '/setup', component: SetupWizardPage });

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (search: Record<string, unknown>): { step: string | undefined; notice?: string } => ({
    step: typeof search.step === 'string' ? search.step : undefined,
    // A one-line reason the person landed here, e.g. 'lock_ended' (backlog 3.93).
    notice: typeof search.notice === 'string' ? search.notice : undefined,
  }),
  component: LoginPage,
});

// The forgotten-password pages are public (backlog 7.74). The reset link
// carries its token in the fragment (#token=…), which no route parses: the
// page reads it, and it never reaches the server in a URL. The invitation
// link does the same (/invite#token=…, backlog 7.80); invitation links
// mailed earlier (/invite/{token}) are rewritten to that form, so the token
// leaves the path (and the request log) as soon as the SPA loads.
const forgotPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/forgot-password',
  component: ForgotPasswordPage,
});
const resetPasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/reset-password',
  component: ResetPasswordPage,
});

const inviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invite',
  component: InviteAcceptPage,
});
const legacyInviteRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/invite/$token',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/invite', hash: `token=${params.token}`, replace: true });
  },
});

// /account is the personal settings area: one layout (the shell with the
// settings navigation) around Security, Appearance, Profile, API tokens and
// My activity.
// The forced pages below (/account/enrol, /password, /lock-pin) are not part
// of it: they render without the shell.
const accountRoute = createRoute({ getParentRoute: () => rootRoute, path: '/account', component: AccountLayout });
const accountIndexRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/account/security', replace: true });
  },
});
const accountSecurityRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: '/security',
  component: AccountSecurityPage,
});
const accountAppearanceRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: '/appearance',
  component: AppearancePage,
});
const accountProfileRoute = createRoute({ getParentRoute: () => accountRoute, path: '/profile', component: ProfilePage });
const accountTokensRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: '/tokens',
  validateSearch: (s: Record<string, unknown>) => validateListSearch(s),
  component: PersonalTokensPage,
});
const accountActivityRoute = createRoute({
  getParentRoute: () => accountRoute,
  path: '/activity',
  validateSearch: validateMyActivitySearch,
  component: MyActivityPage,
});

// /account/enrol is where a session whose account has no second factor yet
// is sent (a second factor is mandatory): it renders without the app shell,
// since such a session cannot load organizations.
const enrolRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/account/enrol',
  component: EnrolmentLayout,
});

// /account/password is where a session whose account must replace a
// password an administrator set is sent; like enrolment, it renders
// without the app shell.
const changePasswordRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/account/password',
  component: ChangePasswordPage,
});

// /account/lock-pin is the one-time offer to set a lock PIN after signing
// in; a signed-in page without the app shell, like the two above.
const lockPinRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/account/lock-pin',
  component: LockPinPromptPage,
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
  // The list's sort and page (?sort=&order=&page=, features/lists) and its
  // filters (zone, managed, monitored, activeRuns) sit beside it.
  validateSearch: validateHostsSearch,
  component: HostsListPage,
});
const hostDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/hosts/$id',
  // The open tab (?tab=checks); Overview, the default, is left out.
  validateSearch: validateHostSearch,
  component: HostDetailPage,
});

// fleetRoute is the fleet overview (v0.3b-2 spec, "Fleet page"): a
// top-level entry beside Hosts rather than nested under /checks, since it
// is a view across every host's own state, not one more page about the
// check/rule pipeline's configuration.
const fleetRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/fleet',
  validateSearch: validateFleetSearch,
  component: FleetPage,
});

// alertsRoute is the Alerts list (v0.4a design doc, "GUI"): alongside Fleet
// rather than nested under /checks — it reads what the rule engine
// concluded (open episodes, not configuration), the same reasoning
// fleetRoute's own comment gives for sitting beside Checks instead of under it.
//
// `?host=` is HostAlertsCard's own escape hatch (v0.4a Task 10 fix round
// 2): that card scopes itself to one host's OPEN alerts only — its own
// empty state ("this host is healthy") is unreachable otherwise — so the
// only way back to a host's closed-alert history is a link here, and that
// link has to land pre-scoped rather than on the unscoped full list.
// Every other filter of the list sits beside it (alertsSearch.ts).
const alertsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/alerts',
  validateSearch: validateAlertsSearch,
  component: AlertsPage,
});

const groupsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/groups',
  validateSearch: validateGroupsSearch,
  component: GroupsPage,
});
const groupDetailRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/groups/$id',
  // The open tab (?tab=members); Variables, the default, is left out. The
  // earlier pages' names (?tab=values, ?tab=children) are read as the tab
  // they became.
  validateSearch: validateGroupSearch,
  // A section anchor of the page before the tabs (#members, bookmarks and
  // older docs) is sent on to its tab.
  beforeLoad: ({ search, params, location }) => {
    const former = search.tab === undefined ? formerSection(location.hash) : undefined;
    if (former && former !== 'variables') throw redirect({ to: '/o/$org/groups/$id', params, search: { tab: former }, hash: '', replace: true });
  },
  component: GroupDetailPage,
});

const reposRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/repos',
  validateSearch: validateReposSearch,
  component: ReposPage,
});
const playbooksRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/playbooks',
  validateSearch: validatePlaybooksSearch,
  component: PlaybooksPage,
});
const templatesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/templates',
  validateSearch: validateTemplatesSearch,
  component: TemplatesPage,
});
const templateVariablesRoute = createRoute({ getParentRoute: () => orgRoute, path: '/templates/$id/variables', component: TemplateVariablesPage });
const schedulesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/schedules',
  validateSearch: validateSchedulesSearch,
  component: SchedulesPage,
});
const runsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/runs',
  validateSearch: validateRunsSearch,
  component: RunsPage,
});
const runDetailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: RunDetailPage });
// Run requests (spec 2026-11-16): the runs an assistant asked for, and the
// page where a person approves or rejects one. The static segment wins over
// /runs/$id, so no run id is ever read as "requests".
const runRequestsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/runs/requests',
  validateSearch: validateRunRequestsSearch,
  component: RunRequestsPage,
});
const runRequestDetailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/requests/$id', component: RunRequestDetailPage });
const workersRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/workers',
  validateSearch: validateWorkersSearch,
  component: WorkersPage,
});
const agentsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/agents',
  validateSearch: validateAgentsSearch,
  component: AgentsPage,
});
const maintenanceRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/maintenance',
  validateSearch: validateMaintenanceSearch,
  component: MaintenancePage,
});

/**
 * Credentials and Jump hosts are top-level pages (v0.4f), in the
 * sidebar's Infrastructure group: they are how a run reaches a host, not
 * a property of the installation, and a page that no longer appears in
 * the Settings tab strip must not keep living underneath /settings —
 * landing there would show a strip with no tab for the page you are on.
 *
 * The paths they vacated redirect here rather than 404: they are in
 * bookmarks, in older plan documents, and in anything anyone pasted to a
 * colleague.
 */
const credentialsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/credentials',
  validateSearch: validateCredentialsSearch,
  component: CredentialsPage,
});
const jumpHostsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/jump-hosts',
  validateSearch: validateJumpHostsSearch,
  component: JumpHostsPage,
});

/**
 * checksRoute is the layout route for the monitoring check pipeline
 * (v0.3b-1, joined by v0.3b-2's rules and v0.3b-3's items): the
 * definitions themselves at its index, the monitoring groups they bind to,
 * the rules that turn a measured value into a warning or critical state,
 * and the decisions that keep an item from being judged at all, all as
 * sibling pages underneath it. Nesting them under /checks rather than
 * giving each a second top-level entry is the same decision settingsRoute encodes — one
 * sidebar entry, a tab strip inside (features/checks/ChecksTabs.tsx) — and
 * it is what lets the shell's Checks nav link mark itself active on every
 * one of them through the router's own activeProps.
 */
const checksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/checks' });
const checksIndexRoute = createRoute({
  getParentRoute: () => checksRoute,
  path: '/',
  validateSearch: validateChecksSearch,
  component: ChecksPage,
});
const monitoringGroupsRoute = createRoute({
  getParentRoute: () => checksRoute,
  path: '/monitoring-groups',
  validateSearch: validateMonitoringGroupsSearch,
  component: MonitoringGroupsPage,
});
const rulesRoute = createRoute({
  getParentRoute: () => checksRoute,
  path: '/rules',
  validateSearch: validateRulesSearch,
  component: RulesPage,
});
const itemsRoute = createRoute({
  getParentRoute: () => checksRoute,
  path: '/items',
  validateSearch: validateDecisionsSearch,
  component: ItemsPage,
});

const routeTree = rootRoute.addChildren([
  indexRoute,
  setupRoute,
  loginRoute,
  forgotPasswordRoute,
  resetPasswordRoute,
  inviteRoute,
  legacyInviteRoute,
  accountRoute.addChildren([accountIndexRoute, accountSecurityRoute, accountAppearanceRoute, accountProfileRoute, accountTokensRoute, accountActivityRoute]),
  enrolRoute,
  lockPinRoute,
  changePasswordRoute,
  orgRoute.addChildren([
    orgIndexRoute,
    hostsRoute,
    hostDetailRoute,
    fleetRoute,
    alertsRoute,
    groupsRoute,
    groupDetailRoute,
    reposRoute,
    playbooksRoute,
    templatesRoute,
    templateVariablesRoute,
    schedulesRoute,
    runsRoute,
    runDetailRoute,
    runRequestsRoute,
    runRequestDetailRoute,
    workersRoute,
    agentsRoute,
    maintenanceRoute,
    credentialsRoute,
    jumpHostsRoute,
    checksRoute.addChildren([checksIndexRoute, monitoringGroupsRoute, rulesRoute, itemsRoute]),
    settingsRouteTree(orgRoute),
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
