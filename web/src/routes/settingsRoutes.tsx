import { createRoute, redirect, type AnyRoute } from '@tanstack/react-router';
import { MembersPage } from '../features/members/MembersPage.tsx';
import { MemberPage } from '../features/members/MemberPage.tsx';
import { UsersPage } from '../features/users/UsersPage.tsx';
import { AccountPage } from '../features/users/AccountPage.tsx';
import { OrganizationsPage } from '../features/instance/OrganizationsPage.tsx';
import { ActivityPage } from '../features/activity/ActivityPage.tsx';
import { TokensPage } from '../features/tokens/TokensPage.tsx';
import { SmtpSettingsPage } from '../features/instance/SmtpSettingsPage.tsx';
import { LanguageSettingsPage } from '../features/instance/LanguageSettingsPage.tsx';
import { BackupsPage } from '../features/instance/BackupsPage.tsx';
import { SharedReposPage } from '../features/instance/shared/SharedReposPage.tsx';
import { SharedRepoFormPage } from '../features/instance/shared/SharedRepoFormPage.tsx';
import { SharedTemplatesPage } from '../features/instance/shared/SharedTemplatesPage.tsx';
import { SharedTemplateFormPage } from '../features/instance/shared/SharedTemplateFormPage.tsx';
import { SharedChecksPage } from '../features/instance/shared/SharedChecksPage.tsx';
import { SharedCheckFormPage } from '../features/instance/shared/SharedCheckFormPage.tsx';
import { SettingsLayout } from '../features/settings/SettingsLayout.tsx';
import { OrgGeneralPage } from '../features/settings/OrgGeneralPage.tsx';
import { ChannelsPage } from '../features/notifications/ChannelsPage.tsx';
import { AlertRoutesPage } from '../features/notifications/AlertRoutesPage.tsx';
import { RotationsPage } from '../features/notifications/RotationsPage.tsx';

/**
 * The Settings subtree of the organization routes: the layout route with its
 * tabs, the instance-administration pages filed under it and the redirects
 * from the paths that moved away. Split out of router.tsx (backlog 7.77) so
 * the route table stays under the file-size signal; `orgRoute` is the parent.
 */
export function settingsRouteTree(orgRoute: AnyRoute) {
  /**
   * settingsRoute is the layout route (SettingsLayout: a grouped side
   * navigation beside the page) for General, Members, Activity, API tokens,
   * outgoing mail and the notification pages (channels, alert routes,
   * on-call rotations) — finding "Important 6": the mockup
   * (Members.dc.html, Activity.dc.html) files them under one "Settings"
   * section (gear icon, single nav entry) with breadcrumbs like
   * "Settings › Members", not as separate top-level nav items. Credentials
   * and Jump hosts were here too until v0.4f; see their own comment in router.tsx. Nesting them under one real route both matches
   * that IA and lets the shell's Settings nav link mark itself active via
   * the router's own activeProps (matching any descendant of /settings)
   * instead of hand-rolled pathname string matching.
   */
  const settingsRoute = createRoute({
    getParentRoute: () => orgRoute,
    path: '/settings',
    component: SettingsLayout,
  });
  const settingsIndexRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/',
    beforeLoad: ({ params }) => {
      throw redirect({ to: '/o/$org/settings/members', params: { org: params.org } });
    },
  });
  const generalRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/general', component: OrgGeneralPage });
  const membersRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/members', component: MembersPage });
  // A member's page in this organization: the role, and for an instance
  // administrator the account's sign-in and actions too (backlog 7.74).
  const memberRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/members/$userId', component: MemberPage });
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
  // Every account on the instance and one account's page, for instance
  // administrators; filed under Settings for navigation only, same as mail.
  const usersRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/users', component: UsersPage });
  const accountRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/users/$userId', component: AccountPage });
  // Every organization on the instance, for instance administrators; filed
  // under Settings for navigation only, same as users.
  const organizationsRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/organizations',
    component: OrganizationsPage,
  });
  // The instance's default GUI language (backlog 1.41), for instance
  // administrators; filed under Settings for navigation only, same as mail.
  const languageRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/language',
    component: LanguageSettingsPage,
  });
  // Instance backups (backlog 7.2): destination, schedule and the list of
  // backups, for instance administrators; filed under Settings for navigation
  // only, same as mail.
  const backupsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/backups', component: BackupsPage });
  // Instance-wide shared resources (v0.2f), for instance administrators; filed
  // under Settings for navigation only, same as mail above. Each kind has a
  // list page and a form page (`new` creates, `$id` edits); `/shared` and the
  // former single page `/shared-resources` land on the first list.
  const sharedRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/shared',
    beforeLoad: ({ params }) => {
      throw redirect({ to: '/o/$org/settings/shared/repos', params: { org: params.org } });
    },
  });
  const legacySharedResourcesRoute = createRoute({
    getParentRoute: () => settingsRoute,
    path: '/shared-resources',
    beforeLoad: ({ params }) => {
      throw redirect({ to: '/o/$org/settings/shared/repos', params: { org: params.org } });
    },
  });
  const sharedReposRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/repos', component: SharedReposPage });
  const sharedRepoNewRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/repos/new', component: SharedRepoFormPage });
  const sharedRepoEditRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/repos/$id', component: SharedRepoFormPage });
  const sharedTemplatesRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/templates', component: SharedTemplatesPage });
  const sharedTemplateNewRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/templates/new', component: SharedTemplateFormPage });
  const sharedTemplateEditRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/templates/$id', component: SharedTemplateFormPage });
  const sharedChecksRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/checks', component: SharedChecksPage });
  const sharedCheckNewRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/checks/new', component: SharedCheckFormPage });
  const sharedCheckEditRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/shared/checks/$id', component: SharedCheckFormPage });
  const notificationsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/notifications', component: ChannelsPage });
  const alertRoutesRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/alert-routes', component: AlertRoutesPage });
  const rotationsRoute = createRoute({ getParentRoute: () => settingsRoute, path: '/rotations', component: RotationsPage });

  return settingsRoute.addChildren([
        settingsIndexRoute,
        generalRoute,
        membersRoute,
        memberRoute,
        activityRoute,
        tokensRoute,
        legacyCredentialsRoute,
        legacyJumpHostsRoute,
        usersRoute,
        accountRoute,
        organizationsRoute,
        mailRoute,
        languageRoute,
        backupsRoute,
        sharedRoute,
        legacySharedResourcesRoute,
        sharedReposRoute,
        sharedRepoNewRoute,
        sharedRepoEditRoute,
        sharedTemplatesRoute,
        sharedTemplateNewRoute,
        sharedTemplateEditRoute,
        sharedChecksRoute,
        sharedCheckNewRoute,
        sharedCheckEditRoute,
        notificationsRoute,
        alertRoutesRoute,
        rotationsRoute,
  ]);
}
