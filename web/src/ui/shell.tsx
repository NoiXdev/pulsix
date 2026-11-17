import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link, useNavigate } from '@tanstack/react-router';
import { Bell, Lock, MoreHorizontal, Search } from 'lucide-react';
import { PulsixMark } from './PulsixMark.tsx';
import { Select } from './Select.tsx';
import { OrgNav, SettingsNav } from './shell-nav.tsx';
import { useT } from '../i18n/useT.ts';

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

/** The personal settings area's sidebar: a way back, who the person is and
 * the pages of the area (see SettingsNav). */
export interface ShellSettingsNav {
  /** The organization "Back to …" returns to. */
  backOrg: ShellOrg;
  personName: string;
  personRole: string;
}

export interface AppShellProps {
  org: ShellOrg;
  /** Every organization the signed-in user belongs to, for the org
   * switcher — finding "Important 6": this used to be a button that did
   * nothing. */
  orgs: ShellOrg[];
  onSelectOrg: (slug: string) => void;
  /** Adds "+ New organization" as the switcher's last entry and calls this
   * when it is chosen. Only an instance admin may create an organization,
   * so a caller passes it for no one else (backlog 7.74). */
  onCreateOrg?: () => void;
  /** Adds the lock button to the top bar and calls this when it is pressed
   * (backlog 3.93). The shell only draws it: locking needs the API, which
   * src/ui may not import, so the route-connected layout passes it in. */
  onLockSession?: () => void;
  crumbs: ReactNode;
  user: ShellUser;
  /** Finding "Important 6": there was no logout anywhere in the shell and
   * nothing linked to /account/security, so the user footer was dead. Both
   * live in the footer menu below, as the mockup shows. */
  onLogout: () => void;
  /** The manager's build version (GET /setup/status), shown beside the
   * wordmark; nothing is shown while it is unknown. */
  version?: string;
  /** How many run requests wait for a person's decision, shown beside the
   * Runs entry; nothing is shown while it is unknown or zero. */
  pendingRunRequests?: number;
  /** A strip in the normal flow between the header and the page (e.g. the
   * new-version banner); it takes its own space. */
  notice?: ReactNode;
  /** Present: the sidebar shows the settings navigation instead of the
   * organization switcher and navigation. */
  settingsNav?: ShellSettingsNav;
  children: ReactNode;
}

/** displayVersion prefixes a numeric release version with "v" ("0.1.0"
 * reads "v0.1.0"); one that already has it, or a build name such as
 * "dev", stays as it is. */
function displayVersion(version: string): string {
  return /^\d/.test(version) ? `v${version}` : version;
}

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
export function AppShell({ org, orgs, onSelectOrg, onCreateOrg, onLockSession, crumbs, user, onLogout, version, pendingRunRequests, notice, settingsNav, children }: AppShellProps) {
  const navigate = useNavigate();
  const t = useT();
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
          {version && (
            <span title={t.shell.version} className="ml-auto truncate font-mono text-[11px] text-muted-foreground">
              {displayVersion(version)}
            </span>
          )}
        </div>

        {!settingsNav && (
          <>
            {/* The switcher is handed the organizations the signed-in user
                belongs to. A create row ends it only when the caller passes
                onCreateOrg, which is for instance admins alone: an organization
                is the most deliberate object in the product (v0.4g Move 3). */}
            {/* The <label> is a sibling of Select, not a wrapper around it, with
                htmlFor carrying the association instead: an <li role="option">
                in the dropdown below is not interactive content, so wrapping it
                inside the label would make clicking a row trigger the label's
                own activation behaviour — a synthetic click at the input that
                reopens the list downshift just closed (its InputClick reducer
                toggles isOpen unconditionally). A native <select> was
                interactive content, so this never showed up there. Nesting also
                pollutes the label's own computed text with whatever text content
                Select renders inside itself (its toggle button's sr-only "Open"
                label, for one), which a sibling avoids outright. */}
            <div className="mx-3 mt-3 mb-1 flex flex-col gap-0.5 text-left">
              <label htmlFor="org-switcher" className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
                {t.shell.organization}
              </label>
              <Select
                id="org-switcher"
                options={(orgs.length > 0 ? orgs : [org]).map((o) => ({ value: o.slug, label: o.name }))}
                value={org.slug}
                onChange={onSelectOrg}
                create={onCreateOrg ? { label: t.shell.newOrganization, onSelect: onCreateOrg } : undefined}
              />
            </div>
          </>
        )}
        {settingsNav ? <SettingsNav nav={settingsNav} /> : <OrgNav org={org} pendingRunRequests={pendingRunRequests} />}

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
              aria-label={t.shell.accountMenu}
              className="flex cursor-pointer list-none items-center justify-center text-muted-foreground [&::-webkit-details-marker]:hidden"
            >
              <MoreHorizontal size={16} />
            </summary>
            <div className="absolute bottom-7 right-0 z-10 flex w-44 flex-col gap-0.5 rounded-md border border-border bg-card p-1 shadow-lg">
              <Link
                to="/account/security"
                className="rounded px-2.5 py-1.5 text-left text-xs text-foreground hover:bg-background"
              >
                {t.shell.settingsMenu}
              </Link>
              {/* Served by the manager, not an SPA route: a plain link. It
                  answers only an MFA-complete session, like the shell. */}
              <a
                href="/api/explorer"
                target="_blank"
                rel="noopener noreferrer"
                className="rounded px-2.5 py-1.5 text-left text-xs text-foreground hover:bg-background"
              >
                {t.shell.apiExplorer}
                <span className="sr-only"> {t.shell.opensInNewTab}</span>
              </a>
              <button
                type="button"
                onClick={onLogout}
                className="rounded px-2.5 py-1.5 text-left text-xs text-destructive hover:bg-destructive/10"
              >
                {t.shell.logOut}
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
              placeholder={t.shell.search.placeholder}
              aria-label={t.shell.search.label}
              className="flex-1 truncate bg-transparent text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
            />
            <span className="rounded border border-border px-1.5 py-px font-mono text-[11px]">⌘K</span>
          </form>
          {/* Backlog 1.12: the global zone control that used to sit here was
              a label and a chevron that opened nothing. Only two endpoints
              take a zone filter, the fleet page already has its own, and the
              hosts list now has one too — a control on the page it acts on
              beats a global one that would do nothing on most of them. */}
          {onLockSession && (
            <button
              type="button"
              aria-label={t.shell.lockSession}
              title={t.shell.lockSession}
              onClick={onLockSession}
              className="flex h-[34px] w-[34px] items-center justify-center rounded-md border border-border bg-card text-muted-foreground hover:text-foreground"
            >
              <Lock size={16} />
            </button>
          )}
          <button
            type="button"
            aria-label={t.shell.notifications}
            className="relative flex h-[34px] w-[34px] items-center justify-center rounded-md border border-border bg-card text-muted-foreground"
          >
            <Bell size={16} />
          </button>
        </header>

        {notice}

        <main className="flex flex-1 flex-col gap-5 overflow-auto p-6">{children}</main>
      </div>
    </div>
  );
}
