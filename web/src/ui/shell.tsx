import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { Activity, Bell, CalendarClock, ChevronDown, ClipboardList, Cpu, FileCode2, Gauge, GitBranch, KeyRound, Layers, MoreHorizontal, Play, Radio, Search, Server, Settings as SettingsIcon, Waypoints, Wrench } from 'lucide-react';
import { PulsixMark } from './PulsixMark.tsx';
import { NavGroup } from './NavGroup.tsx';
import { may } from '../lib/roles.ts';

export interface ShellOrg {
  slug: string;
  name: string;
  /** The caller's role in this organization; gates nav entries whose
   * page the role could not open (Workers needs workers.read). */
  role?: string;
}

export interface ShellUser {
  displayName: string;
  /** e.g. "Owner · passkey" — role plus second-factor hint, matching the
   * mockup's sidebar footer. */
  subtitle: string;
}

export interface AppShellProps {
  org: ShellOrg;
  /** Every organization the signed-in user belongs to, for the org
   * switcher — finding "Important 6": this used to be a button that did
   * nothing. */
  orgs: ShellOrg[];
  onSelectOrg: (slug: string) => void;
  crumbs: ReactNode;
  user: ShellUser;
  /** Finding "Important 6": there was no logout anywhere in the shell and
   * nothing linked to /account/security, so the user footer was dead. Both
   * live in the footer menu below, as the mockup shows. */
  onLogout: () => void;
  children: ReactNode;
}

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

function initials(name: string): string {
  const parts = name.trim().split(/\s+/);
  return parts
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase() ?? '')
    .join('');
}

/**
 * AppShell is the 232px-sidebar / top-bar frame every authenticated page
 * renders inside — design/mockups/build.mjs's `shell()` generator is the
 * visual spec this follows: wordmark, organization switcher, nav with an
 * active-item indicator, a search field with the ⌘K hint, a static zone
 * filter and notifications icon in the top bar, and a user footer.
 *
 * The nav sits in three labelled groups (v0.4f) — Infrastructure,
 * Monitoring, Automation — with Settings as a single ungrouped entry
 * below them. Flat, the fourteen entries had grown past the point where
 * their order said anything. The headings separate; they do not collapse,
 * so no page costs a second click. NavGroup drops a group whose every
 * entry a role may not see, heading included.
 *
 * Members, Activity and API tokens live under one Settings entry
 * (finding "Important 6"), matching the mockup's own IA rather than
 * sitting as separate top-level items.
 *
 * Every nav link uses the router's own `activeProps` (finding "Important
 * 11") instead of a hand-derived `active` prop: Hosts only matches its
 * own route (so a route literally named /hosts-inc/members can never
 * light it up by substring accident), and Settings matches any
 * descendant of /o/$org/settings, which is exactly the set of routes the
 * mockup groups under it.
 */
export function AppShell({ org, orgs, onSelectOrg, crumbs, user, onLogout, children }: AppShellProps) {
  const navigate = useNavigate();
  const [search, setSearch] = useState('');
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
        e.preventDefault();
        searchRef.current?.focus();
      }
    }
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, []);

  function runSearch() {
    void navigate({ to: '/o/$org/hosts', params: { org: org.slug }, search: { q: search || undefined } });
  }

  return (
    <div className="flex h-screen bg-background text-foreground text-[13px]">
      <aside className="flex w-[232px] shrink-0 flex-col border-r border-border bg-card">
        <div className="flex h-14 items-center gap-2.5 border-b border-border px-4">
          <PulsixMark size={24} />
          <span className="font-mono text-[17px] font-extrabold tracking-tight">
            pulsi<span className="text-brand">x</span>
          </span>
        </div>

        <label className="mx-3 mt-3 mb-1 flex items-center justify-between rounded-md border border-border bg-background px-2.5 py-2 text-left">
          <span className="flex flex-col gap-0.5">
            <span className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
              Organization
            </span>
            <select
              aria-label="Organization"
              value={org.slug}
              onChange={(e) => onSelectOrg(e.target.value)}
              className="appearance-none bg-transparent text-[13px] font-semibold text-foreground outline-none"
            >
              {(orgs.length > 0 ? orgs : [org]).map((o) => (
                <option key={o.slug} value={o.slug} className="bg-card text-foreground">
                  {o.name}
                </option>
              ))}
            </select>
          </span>
          <ChevronDown size={14} className="text-muted-foreground" />
        </label>

        {/* The three headings and their spacing add about 100px to a
            sidebar that already ran to roughly 700px on fourteen
            entries, so the nav scrolls on its own rather than pushing
            the account footer off a short viewport. min-h-0 is what
            lets a flex child shrink below its content height at all. */}
        <nav className="flex min-h-0 flex-col overflow-y-auto px-3 py-2">
          <NavGroup label="Infrastructure">
            <Link to="/o/$org/hosts" params={{ org: org.slug }} search={{ q: undefined }} {...NAV_LINK_PROPS}>
              <Server size={16} />
              <span className="flex-1">Hosts</span>
            </Link>
            <Link to="/o/$org/groups" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <Layers size={16} />
              <span className="flex-1">Groups</span>
            </Link>
            {/* Jump hosts and Credentials were Settings tabs until v0.4f.
                They are how a run reaches a host, not a property of the
                installation, so they belong beside the hosts they serve —
                and their routes moved out of /settings with them. */}
            <Link to="/o/$org/jump-hosts" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <Waypoints size={16} />
              <span className="flex-1">Jump hosts</span>
            </Link>
            <Link to="/o/$org/credentials" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <KeyRound size={16} />
              <span className="flex-1">Credentials</span>
            </Link>
            {may(org.role, 'agents.read') && (
              <Link to="/o/$org/agents" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
                <Radio size={16} />
                <span className="flex-1">Agents</span>
              </Link>
            )}
            {may(org.role, 'workers.read') && (
              <Link to="/o/$org/workers" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
                <Cpu size={16} />
                <span className="flex-1">Workers</span>
              </Link>
            )}
          </NavGroup>

          <NavGroup label="Monitoring">
            {/* Fleet (v0.3b-2) is a view across every host's own monitoring
                state, gated on the same checks.read the rest of the rule
                surface uses — a viewer reads it, same as the Checks entry
                below. */}
            {may(org.role, 'checks.read') && (
              <Link to="/o/$org/fleet" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
                <Gauge size={16} />
                <span className="flex-1">Fleet</span>
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
                <span className="flex-1">Alerts</span>
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
                <span className="flex-1">Checks</span>
              </Link>
            )}
            <Link to="/o/$org/maintenance" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <Wrench size={16} />
              <span className="flex-1">Maintenance</span>
            </Link>
          </NavGroup>

          <NavGroup label="Automation">
            <Link to="/o/$org/repos" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <GitBranch size={16} />
              <span className="flex-1">Repositories</span>
            </Link>
            <Link to="/o/$org/playbooks" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <FileCode2 size={16} />
              <span className="flex-1">Playbooks</span>
            </Link>
            <Link to="/o/$org/templates" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <ClipboardList size={16} />
              <span className="flex-1">Templates</span>
            </Link>
            <Link
              to="/o/$org/schedules"
              params={{ org: org.slug }}
              search={{ id: undefined, template: undefined }}
              {...NAV_LINK_PROPS}
              activeOptions={{ exact: false, includeSearch: false }}
            >
              <CalendarClock size={16} />
              <span className="flex-1">Schedules</span>
            </Link>
            <Link
              to="/o/$org/runs"
              params={{ org: org.slug }}
              search={{ status: undefined, kind: undefined, trigger: undefined, schedule: undefined }}
              {...NAV_LINK_PROPS}
            >
              <Play size={16} />
              <span className="flex-1">Runs</span>
            </Link>
          </NavGroup>

          {/* Settings stays one ungrouped entry: a heading over a single
              link would name a group of one. */}
          <div className="mt-4 flex flex-col gap-0.5">
            <Link to="/o/$org/settings" params={{ org: org.slug }} {...NAV_LINK_PROPS}>
              <SettingsIcon size={16} />
              <span className="flex-1">Settings</span>
            </Link>
          </div>
        </nav>

        <div className="mt-auto flex items-center gap-2.5 border-t border-border px-4 py-3.5">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-brand-dim text-[11px] font-bold text-background">
            {initials(user.displayName)}
          </span>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate text-xs font-semibold">{user.displayName}</span>
            <span className="truncate text-[11px] text-muted-foreground">{user.subtitle}</span>
          </span>
          <details className="relative">
            <summary
              role="button"
              aria-label="Account menu"
              className="flex cursor-pointer list-none items-center justify-center text-muted-foreground [&::-webkit-details-marker]:hidden"
            >
              <MoreHorizontal size={16} />
            </summary>
            <div className="absolute bottom-7 right-0 z-10 flex w-44 flex-col gap-0.5 rounded-md border border-border bg-card p-1 shadow-lg">
              <Link
                to="/account/security"
                className="rounded px-2.5 py-1.5 text-left text-xs text-foreground hover:bg-background"
              >
                Account security
              </Link>
              <button
                type="button"
                onClick={onLogout}
                className="rounded px-2.5 py-1.5 text-left text-xs text-destructive hover:bg-destructive/10"
              >
                Log out
              </button>
            </div>
          </details>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-4 border-b border-border px-6">
          <div className="flex items-center gap-2 text-[13px] text-muted-foreground">{crumbs}</div>
          <form
            className="ml-auto flex h-[34px] w-[300px] items-center gap-2.5 rounded-md border border-border bg-card px-3 text-muted-foreground"
            onSubmit={(e) => {
              e.preventDefault();
              runSearch();
            }}
          >
            <Search size={15} />
            <input
              ref={searchRef}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search hosts…"
              aria-label="Search hosts"
              className="flex-1 truncate bg-transparent text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
            />
            <span className="rounded border border-border px-1.5 py-px font-mono text-[11px]">⌘K</span>
          </form>
          {/* Backlog 1.12: the global zone control that used to sit here was
              a label and a chevron that opened nothing. Only two endpoints
              take a zone filter, the fleet page already has its own, and the
              hosts list now has one too — a control on the page it acts on
              beats a global one that would do nothing on most of them. */}
          <button
            type="button"
            aria-label="Notifications"
            className="relative flex h-[34px] w-[34px] items-center justify-center rounded-md border border-border bg-card text-muted-foreground"
          >
            <Bell size={16} />
          </button>
        </header>

        <main className="flex flex-1 flex-col gap-5 overflow-auto p-6">{children}</main>
      </div>
    </div>
  );
}
