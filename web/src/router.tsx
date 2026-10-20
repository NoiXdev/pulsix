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
import { FleetPage } from './features/fleet/FleetPage.tsx';
import { AlertsPage } from './features/alerts/AlertsPage.tsx';
import { GroupsPage } from './features/inventory/GroupsPage.tsx';
import { GroupDetailPage } from './features/inventory/GroupDetailPage.tsx';
import { MembersPage } from './features/members/MembersPage.tsx';
import { ActivityPage } from './features/activity/ActivityPage.tsx';
import { TokensPage } from './features/tokens/TokensPage.tsx';
import { SmtpSettingsPage } from './features/instance/SmtpSettingsPage.tsx';
import { SharedResourcesPage } from './features/instance/SharedResourcesPage.tsx';
import { CredentialsPage } from './features/credentials/CredentialsPage.tsx';
import { JumpHostsPage } from './features/credentials/JumpHostsPage.tsx';
import { ReposPage } from './features/repos/ReposPage.tsx';
import { PlaybooksPage } from './features/playbooks/PlaybooksPage.tsx';
import { TemplatesPage } from './features/templates/TemplatesPage.tsx';
import { SchedulesPage } from './features/schedules/SchedulesPage.tsx';
import { RunsPage } from './features/runs/RunsPage.tsx';
import { RunDetailPage } from './features/runs/RunDetailPage.tsx';
import { WorkersPage } from './features/workers/WorkersPage.tsx';
import { AgentsPage } from './features/agents/AgentsPage.tsx';
import { ChecksPage } from './features/checks/ChecksPage.tsx';
import { MonitoringGroupsPage } from './features/checks/MonitoringGroupsPage.tsx';
import { RulesPage } from './features/checks/RulesPage.tsx';
import { ItemsPage } from './features/checks/ItemsPage.tsx';
import { ChannelsPage } from './features/notifications/ChannelsPage.tsx';
import { AlertRoutesPage } from './features/notifications/AlertRoutesPage.tsx';
import { RotationsPage } from './features/notifications/RotationsPage.tsx';
import { MaintenancePage } from './features/maintenance/MaintenancePage.tsx';

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

// fleetRoute is the fleet overview (v0.3b-2 spec, "Fleet page"): a
// top-level entry beside Hosts rather than nested under /checks, since it
// is a view across every host's own state, not one more page about the
// check/rule pipeline's configuration.
const fleetRoute = createRoute({ getParentRoute: () => orgRoute, path: '/fleet', component: FleetPage });

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
const alertsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/alerts',
  validateSearch: (search: Record<string, unknown>) => ({
    host: typeof search.host === 'string' ? search.host : undefined,
  }),
  component: AlertsPage,
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

const reposRoute = createRoute({ getParentRoute: () => orgRoute, path: '/repos', component: ReposPage });
const playbooksRoute = createRoute({ getParentRoute: () => orgRoute, path: '/playbooks', component: PlaybooksPage });
const templatesRoute = createRoute({ getParentRoute: () => orgRoute, path: '/templates', component: TemplatesPage });
const schedulesRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/schedules',
  validateSearch: (search: Record<string, unknown>) => ({
    id: typeof search.id === 'string' ? search.id : undefined,
    template: typeof search.template === 'string' ? search.template : undefined,
  }),
  component: SchedulesPage,
});
const runsRoute = createRoute({
  getParentRoute: () => orgRoute,
  path: '/runs',
  validateSearch: (search: Record<string, unknown>) => ({
    status: typeof search.status === 'string' ? search.status : undefined,
    kind: typeof search.kind === 'string' ? search.kind : undefined,
    trigger: typeof search.trigger === 'string' ? search.trigger : undefined,
    schedule: typeof search.schedule === 'string' ? search.schedule : undefined,
  }),
  component: RunsPage,
});
const runDetailRoute = createRoute({ getParentRoute: () => orgRoute, path: '/runs/$id', component: RunDetailPage });
const workersRoute = createRoute({ getParentRoute: () => orgRoute, path: '/workers', component: WorkersPage });
const agentsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/agents', component: AgentsPage });
const maintenanceRoute = createRoute({ getParentRoute: () => orgRoute, path: '/maintenance', component: MaintenancePage });

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
const credentialsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/credentials', component: CredentialsPage });
const jumpHostsRoute = createRoute({ getParentRoute: () => orgRoute, path: '/jump-hosts', component: JumpHostsPage });

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
const checksIndexRoute = createRoute({ getParentRoute: () => checksRoute, path: '/', component: ChecksPage });
const monitoringGroupsRoute = createRoute({
  getParentRoute: () => checksRoute,
  path: '/monitoring-groups',
  component: MonitoringGroupsPage,
});
const rulesRoute = createRoute({ getParentRoute: () => checksRoute, path: '/rules', component: RulesPage });
const itemsRoute = createRoute({ getParentRoute: () => checksRoute, path: '/items', component: ItemsPage });

/**
 * settingsRoute is the layout route for Members, Activity, API tokens,
 * outgoing mail and the notification pages (channels, alert routes,
 * on-call rotations) — finding "Important 6": the mockup
 * (Members.dc.html, Activity.dc.html) files them under one "Settings"
 * section (gear icon, single nav entry) with breadcrumbs like
 * "Settings › Members", not as separate top-level nav items. Credentials
 * and Jump hosts were here too until v0.4f; see their own comment above. Nesting them under one real route both matches
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
const legacyCredentialsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/credentials',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/o/$org/credentials', params: { org: params.org } });
  },
});
const legacyJumpHostsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/jump-hosts',
  beforeLoad: ({ params }) => {
    throw redirect({ to: '/o/$org/jump-hosts', params: { org: params.org } });
  },
});
// Instance-wide outgoing mail settings, for instance administrators; filed
// under Settings for navigation only (see SmtpSettingsPage).
const mailRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/mail', component: SmtpSettingsPage });
// Instance-wide shared repository definitions (v0.2f), for instance
// administrators; filed under Settings for navigation only, same as mail
// above (see SharedResourcesPage).
const sharedResourcesRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: '/shared-resources',
  component: SharedResourcesPage,
});
const notificationsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/notifications', component: ChannelsPage });
const alertRoutesRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/alert-routes', component: AlertRoutesPage });
const rotationsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/rotations', component: RotationsPage });

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
    fleetRoute,
    alertsRoute,
    groupsRoute,
    groupDetailRoute,
    reposRoute,
    playbooksRoute,
    templatesRoute,
    schedulesRoute,
    runsRoute,
    runDetailRoute,
    workersRoute,
    agentsRoute,
    maintenanceRoute,
    credentialsRoute,
    jumpHostsRoute,
    checksRoute.addChildren([checksIndexRoute, monitoringGroupsRoute, rulesRoute, itemsRoute]),
    settingsRoute.addChildren([
      settingsIndexRoute,
      membersRoute,
      activityRoute,
      tokensRoute,
      legacyCredentialsRoute,
      legacyJumpHostsRoute,
      mailRoute,
      sharedResourcesRoute,
      notificationsRoute,
      alertRoutesRoute,
      rotationsRoute,
    ]),
  ]),
]);

export const router = createRouter({ routeTree, defaultPreload: 'intent' });

declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router;
  }
}
