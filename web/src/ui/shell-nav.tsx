import { Link } from '@tanstack/react-router';
import { Activity, ArrowLeft, Bell, CalendarClock, ClipboardList, Cpu, FileCode2, Gauge, GitBranch, KeyRound, Layers, Palette, Play, Radio, Server, Settings as SettingsIcon, ShieldCheck, UserRound, History, Waypoints, Wrench } from 'lucide-react';
import { NavGroup } from './NavGroup.tsx';
import { may } from '../lib/roles.ts';
import { useT } from '../i18n/useT.ts';
import type { ShellOrg, ShellSettingsNav } from './shell.tsx';

const ACTIVE_LINK =
  'flex h-9 items-center gap-2.5 rounded-md bg-background px-3 font-semibold text-foreground shadow-[inset_2px_0_0_var(--px-accent)]';
const INACTIVE_LINK = 'flex h-9 items-center gap-2.5 rounded-md px-3 font-medium text-muted-foreground hover:text-foreground';

/** The three props every sidebar link repeats. `activeOptions.exact:
 * false` is what lets one entry mark itself on a whole section (Checks
 * on /checks/monitoring-groups, Settings on /settings/members); the
 * router derives the match from the route, never from the pathname
 * (finding "Important 11"). Spread first, so an entry that needs its own
 * activeOptions can override them. */
const NAV_LINK_PROPS = {
  activeOptions: { exact: false },
  activeProps: { className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' },
  inactiveProps: { className: INACTIVE_LINK },
} as const;

/**
 * OrgNav is the organization sidebar: three labelled groups (v0.4f) —
 * Infrastructure, Monitoring, Automation — with Settings as a single
 * ungrouped entry below them. Every link uses the router's own
 * `activeProps` (finding "Important 11"), see NAV_LINK_PROPS. The nav
 * scrolls on its own, so the account footer is not pushed off a short
 * viewport (min-h-0 lets a flex child shrink below its content height).
 */
export function OrgNav({ org, pendingRunRequests }: { org: ShellOrg; pendingRunRequests?: number }) {
  const t = useT();
  return (
    <nav className="flex min-h-0 flex-col overflow-y-auto px-3 py-2">
      <NavGroup label={t.shell.nav.infrastructure}>
        <Link to="/o/$org/hosts" params={{ org: org.slug }} search={{ q: undefined }} {...NAV_LINK_PROPS}>
          <Server size={16} />
          <span className="flex-1">{t.shell.nav.hosts}</span>
        </Link>
        <Link to="/o/$org/groups" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <Layers size={16} />
          <span className="flex-1">{t.shell.nav.groups}</span>
        </Link>
        {/* Jump hosts and Credentials were Settings tabs until v0.4f.
            They are how a run reaches a host, not a property of the
            installation, so they belong beside the hosts they serve —
            and their routes moved out of /settings with them. */}
        <Link to="/o/$org/jump-hosts" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <Waypoints size={16} />
          <span className="flex-1">{t.shell.nav.jumpHosts}</span>
        </Link>
        <Link to="/o/$org/credentials" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <KeyRound size={16} />
          <span className="flex-1">{t.shell.nav.credentials}</span>
        </Link>
        {may(org.role, 'agents.read') && (
          <Link to="/o/$org/agents" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
            <Radio size={16} />
            <span className="flex-1">{t.shell.nav.agents}</span>
          </Link>
        )}
        {may(org.role, 'workers.read') && (
          <Link to="/o/$org/workers" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
            <Cpu size={16} />
            <span className="flex-1">{t.shell.nav.workers}</span>
          </Link>
        )}
      </NavGroup>

      <NavGroup label={t.shell.nav.monitoring}>
        {/* Fleet (v0.3b-2) is a view across every host's own monitoring
            state, gated on the same checks.read the rest of the rule
            surface uses — a viewer reads it, same as the Checks entry
            below. */}
        {may(org.role, 'checks.read') && (
          <Link to="/o/$org/fleet" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
            <Gauge size={16} />
            <span className="flex-1">{t.shell.nav.fleet}</span>
          </Link>
        )}
        {/* Alerts (v0.4a) is the durable-episode view beside Fleet's own
            live roll-up — same checks.read bar as Fleet and Checks, since
            it is a read of what the rule engine already concluded.

            Unscoped: the sidebar's own entry point is the full
            organization-wide list, never a host — host-scoping comes
            only from HostAlertsCard's own link, which sets this. */}
        {may(org.role, 'checks.read') && (
          <Link to="/o/$org/alerts" params={{ org: org.slug }} search={{ host: undefined }} {...NAV_LINK_PROPS}>
            <Bell size={16} />
            <span className="flex-1">{t.shell.nav.alerts}</span>
          </Link>
        )}
        {/* Checks (v0.3b-1) covers both the definitions and the
            monitoring groups they bind to, which live under /checks as
            a tab strip rather than as a second sidebar entry — see
            router.tsx's checksRoute. checks.read is a viewer's right,
            so this is gated only so that a session whose role has not
            loaded yet is not offered a page it cannot open. */}
        {may(org.role, 'checks.read') && (
          <Link to="/o/$org/checks" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
            <Activity size={16} />
            <span className="flex-1">{t.shell.nav.checks}</span>
          </Link>
        )}
        <Link to="/o/$org/maintenance" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <Wrench size={16} />
          <span className="flex-1">{t.shell.nav.maintenance}</span>
        </Link>
      </NavGroup>

      <NavGroup label={t.shell.nav.automation}>
        <Link to="/o/$org/repos" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <GitBranch size={16} />
          <span className="flex-1">{t.shell.nav.repositories}</span>
        </Link>
        <Link to="/o/$org/playbooks" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <FileCode2 size={16} />
          <span className="flex-1">{t.shell.nav.playbooks}</span>
        </Link>
        <Link to="/o/$org/templates" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <ClipboardList size={16} />
          <span className="flex-1">{t.shell.nav.templates}</span>
        </Link>
        <Link
          to="/o/$org/schedules"
          params={{ org: org.slug }}
          search={{ id: undefined, template: undefined }}
          {...NAV_LINK_PROPS}
          activeOptions={{ exact: false, includeSearch: false }}
        >
          <CalendarClock size={16} />
          <span className="flex-1">{t.shell.nav.schedules}</span>
        </Link>
        <Link
          to="/o/$org/runs"
          params={{ org: org.slug }}
          search={{ status: undefined, kind: undefined, trigger: undefined, schedule: undefined, host: undefined }}
          {...NAV_LINK_PROPS}
        >
          <Play size={16} />
          <span className="flex-1">{t.shell.nav.runs}</span>
          {pendingRunRequests !== undefined && pendingRunRequests > 0 && (
            <>
              <span
                aria-hidden="true"
                className="rounded-full bg-primary px-2 py-px font-mono text-[11px] font-semibold text-primary-foreground"
              >
                {pendingRunRequests}
              </span>
              <span className="sr-only">{t.shell.nav.runRequestsBadge(pendingRunRequests)}</span>
            </>
          )}
        </Link>
      </NavGroup>

      {/* Settings stays one ungrouped entry: a heading over a single
          link would name a group of one. */}
      <div className="mt-4 flex flex-col gap-0.5">
        <Link to="/o/$org/settings" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
          <SettingsIcon size={16} />
          <span className="flex-1">{t.shell.nav.settings}</span>
        </Link>
      </div>
    </nav>
  );
}

type SettingsOrgPath =
  | '/o/$org/settings/general'
  | '/o/$org/settings/members'
  | '/o/$org/settings/activity'
  | '/o/$org/settings/tokens'
  | '/o/$org/settings/secrets'
  | '/o/$org/settings/notifications'
  | '/o/$org/settings/alert-routes'
  | '/o/$org/settings/rotations'
  | '/o/$org/settings/users'
  | '/o/$org/settings/organizations'
  | '/o/$org/settings/mail'
  | '/o/$org/settings/language'
  | '/o/$org/settings/backups'
  | '/o/$org/settings/shared/repos'
  | '/o/$org/settings/shared/templates'
  | '/o/$org/settings/shared/checks';

type AccountNavProps = Extract<ShellSettingsNav, { kind: 'account' }>;
type SettingsNavProps = Extract<ShellSettingsNav, { kind: 'settings' }>;

function BackLink({ backOrg }: { backOrg: AccountNavProps['backOrg'] }) {
  const t = useT();
  return (
    <Link
      to="/o/$org/hosts"
      params={{ org: backOrg.slug }}
      search={{ q: undefined }}
      className="flex h-9 items-center gap-2 rounded-md px-3 font-medium text-muted-foreground hover:text-foreground"
    >
      <ArrowLeft size={16} aria-hidden="true" />
      <span className="truncate">{t.account.nav.back(backOrg.name)}</span>
    </Link>
  );
}

/**
 * AccountNav is the sidebar of the personal area (/account/*): a way back
 * to the organization, who the person is, then their own pages.
 */
export function AccountNav({ nav }: { nav: AccountNavProps }) {
  const t = useT();
  return (
    <nav aria-label={t.account.nav.label} className="flex min-h-0 flex-col overflow-y-auto px-3 py-3">
      <BackLink backOrg={nav.backOrg} />
      <div className="mx-3 mt-3 mb-3 flex min-w-0 flex-col border-b border-border pb-3">
        <span className="truncate font-semibold">{nav.personName}</span>
        <span className="truncate text-[11px] text-muted-foreground">{nav.personRole}</span>
      </div>
      <div className="flex flex-col gap-0.5">
        <Link to="/account/security" {...NAV_LINK_PROPS}>
          <ShieldCheck size={16} />
          <span className="flex-1">{t.account.nav.security}</span>
        </Link>
        <Link to="/account/appearance" {...NAV_LINK_PROPS}>
          <Palette size={16} />
          <span className="flex-1">{t.account.nav.appearance}</span>
        </Link>
        <Link to="/account/profile" {...NAV_LINK_PROPS}>
          <UserRound size={16} />
          <span className="flex-1">{t.account.nav.profile}</span>
        </Link>
        <Link to="/account/tokens" {...NAV_LINK_PROPS}>
          <KeyRound size={16} />
          <span className="flex-1">{t.account.nav.tokens}</span>
        </Link>
        <Link to="/account/activity" search={{}} {...NAV_LINK_PROPS}>
          <History size={16} />
          <span className="flex-1">{t.account.nav.activity}</span>
        </Link>
      </div>
    </nav>
  );
}

/**
 * SettingsNav is the sidebar of the organization's settings
 * (/o/$org/settings/*): a way back, a heading, then the Organization
 * group, Alerting (role-gated) and Instance (instance administrators).
 */
export function SettingsNav({ nav }: { nav: SettingsNavProps }) {
  const t = useT();
  const orgLink = (to: SettingsOrgPath, label: string) => (
    <Link key={to} to={to} params={{ org: nav.backOrg.slug }} {...NAV_LINK_PROPS}>
      <span className="flex-1 truncate">{label}</span>
    </Link>
  );
  return (
    <nav aria-label={t.settings.nav.label} className="flex min-h-0 flex-col overflow-y-auto px-3 py-3">
      <BackLink backOrg={nav.backOrg} />
      <div className="mx-3 mt-3 mb-3 flex min-w-0 flex-col border-b border-border pb-3">
        <span className="truncate font-semibold">{t.settings.nav.label}</span>
      </div>
      <NavGroup label={t.settings.nav.organizationIn(nav.backOrg.name)}>
        {orgLink('/o/$org/settings/general', t.settings.tabs.general)}
        {orgLink('/o/$org/settings/members', t.settings.tabs.members)}
        {orgLink('/o/$org/settings/activity', t.settings.tabs.activity)}
        {orgLink('/o/$org/settings/tokens', t.settings.tabs.tokens)}
        {orgLink('/o/$org/settings/secrets', t.settings.tabs.secrets)}
      </NavGroup>
      {nav.showAlerting && (
        <NavGroup label={t.settings.nav.alerting}>
          {orgLink('/o/$org/settings/notifications', t.settings.tabs.notifications)}
          {orgLink('/o/$org/settings/alert-routes', t.settings.tabs.alertRoutes)}
          {orgLink('/o/$org/settings/rotations', t.settings.tabs.rotations)}
        </NavGroup>
      )}
      {nav.showInstance && (
        <NavGroup label={t.settings.nav.instance}>
          {orgLink('/o/$org/settings/users', t.settings.tabs.users)}
          {orgLink('/o/$org/settings/organizations', t.settings.tabs.organizations)}
          {orgLink('/o/$org/settings/mail', t.settings.tabs.mail)}
          {orgLink('/o/$org/settings/language', t.language.instance.tab)}
          {orgLink('/o/$org/settings/backups', t.backups.tab)}
          {orgLink('/o/$org/settings/shared/repos', t.settings.tabs.sharedRepos)}
          {orgLink('/o/$org/settings/shared/templates', t.settings.tabs.sharedTemplates)}
          {orgLink('/o/$org/settings/shared/checks', t.settings.tabs.sharedChecks)}
        </NavGroup>
      )}
    </nav>
  );
}
