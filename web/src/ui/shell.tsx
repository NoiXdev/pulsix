import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { Activity, Bell, CalendarClock, ChevronDown, ClipboardList, Cpu, FileCode2, GitBranch, Globe, Layers, MoreHorizontal, Play, Radio, Search, Server, Settings as SettingsIcon, Wrench } from 'lucide-react';
import { PulsixMark } from './PulsixMark.tsx';
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
 * v0.1's nav is a subset of the full product's (Dashboard, Groups,
 * Playbooks, Jobs, Monitoring, Alerts, Workers and the API Explorer link
 * from the mockup have no backend yet) — see task-15a-report.md for why.
 * Members, Activity and API tokens live under one Settings entry
 * (finding "Important 6"), matching the mockup's own IA rather than
 * sitting as separate top-level items.
 *
 * Both nav links use the router's own `activeProps` (finding "Important
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

        <nav className="flex flex-col gap-0.5 px-3 py-2">
          <Link
            to="/o/$org/hosts"
            params={{ org: org.slug }}
            search={{ q: undefined }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <Server size={16} />
            <span className="flex-1">Hosts</span>
          </Link>
          <Link
            to="/o/$org/groups"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <Layers size={16} />
            <span className="flex-1">Groups</span>
          </Link>
          <Link
            to="/o/$org/repos"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <GitBranch size={16} />
            <span className="flex-1">Repositories</span>
          </Link>
          <Link
            to="/o/$org/playbooks"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <FileCode2 size={16} />
            <span className="flex-1">Playbooks</span>
          </Link>
          <Link
            to="/o/$org/templates"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <ClipboardList size={16} />
            <span className="flex-1">Templates</span>
          </Link>
          <Link
            to="/o/$org/schedules"
            params={{ org: org.slug }}
            search={{ id: undefined, template: undefined }}
            activeOptions={{ exact: false, includeSearch: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <CalendarClock size={16} />
            <span className="flex-1">Schedules</span>
          </Link>
          <Link
            to="/o/$org/maintenance"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <Wrench size={16} />
            <span className="flex-1">Maintenance</span>
          </Link>
          <Link
            to="/o/$org/runs"
            params={{ org: org.slug }}
            search={{ status: undefined, kind: undefined, trigger: undefined, schedule: undefined }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <Play size={16} />
            <span className="flex-1">Runs</span>
          </Link>
          {/* Checks (v0.3b-1) covers both the definitions and the
              monitoring groups they bind to, which live under /checks as
              a tab strip rather than as a second sidebar entry — see
              router.tsx's checksRoute. checks.read is a viewer's right,
              so this is gated only so that a session whose role has not
              loaded yet is not offered a page it cannot open. */}
          {may(org.role, 'checks.read') && (
            <Link
              to="/o/$org/checks"
              params={{ org: org.slug }}
              activeOptions={{ exact: false }}
              activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
              inactiveProps={{ className: INACTIVE_LINK }}
            >
              <Activity size={16} />
              <span className="flex-1">Checks</span>
            </Link>
          )}
          {may(org.role, 'agents.read') && (
            <Link
              to="/o/$org/agents"
              params={{ org: org.slug }}
              activeOptions={{ exact: false }}
              activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
              inactiveProps={{ className: INACTIVE_LINK }}
            >
              <Radio size={16} />
              <span className="flex-1">Agents</span>
            </Link>
          )}
          {may(org.role, 'workers.read') && (
            <Link
              to="/o/$org/workers"
              params={{ org: org.slug }}
              activeOptions={{ exact: false }}
              activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
              inactiveProps={{ className: INACTIVE_LINK }}
            >
              <Cpu size={16} />
              <span className="flex-1">Workers</span>
            </Link>
          )}
          <Link
            to="/o/$org/settings"
            params={{ org: org.slug }}
            activeOptions={{ exact: false }}
            activeProps={{ className: ACTIVE_LINK, 'aria-current': 'page', 'data-nav-active': 'true' }}
            inactiveProps={{ className: INACTIVE_LINK }}
          >
            <SettingsIcon size={16} />
            <span className="flex-1">Settings</span>
          </Link>
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
          <div className="flex h-[34px] items-center gap-2 rounded-md border border-border bg-card px-3 text-foreground">
            <Globe size={15} />
            <span className="text-[13px]">All zones</span>
            <ChevronDown size={14} className="text-muted-foreground" />
          </div>
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
