import type { ReactNode } from 'react';
import { Link } from '@tanstack/react-router';
import { Activity, Bell, ChevronDown, Globe, KeyRound, MoreHorizontal, Search, Server, Users } from 'lucide-react';
import { PulsixMark } from './PulsixMark.tsx';

export type NavKey = 'hosts' | 'members' | 'activity' | 'tokens';

const NAV: Array<{ key: NavKey; label: string; icon: typeof Server }> = [
  { key: 'hosts', label: 'Hosts', icon: Server },
  { key: 'members', label: 'Members', icon: Users },
  { key: 'activity', label: 'Activity', icon: Activity },
  { key: 'tokens', label: 'Settings', icon: KeyRound },
];

export interface ShellOrg {
  slug: string;
  name: string;
}

export interface ShellUser {
  displayName: string;
  /** e.g. "Owner · passkey" — role plus second-factor hint, matching the
   * mockup's sidebar footer. */
  subtitle: string;
}

export interface AppShellProps {
  org: ShellOrg;
  /** Which sidebar item to mark current, or null on a page that is not
   * one of the four org-scoped sections (e.g. /account/security). */
  active: NavKey | null;
  crumbs: ReactNode;
  user: ShellUser;
  onOrgSwitcherClick?: () => void;
  children: ReactNode;
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
 * v0.1's nav is a subset of the full product's (Dashboard, Groups,
 * Playbooks, Jobs, Monitoring, Alerts, Workers and the API Explorer link
 * from the mockup have no backend yet) — see task-15a-report.md for why.
 */
export function AppShell({ org, active, crumbs, user, onOrgSwitcherClick, children }: AppShellProps) {
  return (
    <div className="flex h-screen bg-background text-foreground text-[13px]">
      <aside className="flex w-[232px] shrink-0 flex-col border-r border-border bg-card">
        <div className="flex h-14 items-center gap-2.5 border-b border-border px-4">
          <PulsixMark size={24} />
          <span className="font-mono text-[17px] font-extrabold tracking-tight">
            pulsi<span className="text-accent">x</span>
          </span>
        </div>

        <button
          type="button"
          onClick={onOrgSwitcherClick}
          className="mx-3 mt-3 mb-1 flex items-center justify-between rounded-md border border-border bg-background px-2.5 py-2 text-left"
        >
          <span className="flex flex-col gap-0.5">
            <span className="text-[10px] font-medium tracking-wide text-muted-foreground uppercase">
              Organization
            </span>
            <span className="text-[13px] font-semibold">{org.name}</span>
          </span>
          <ChevronDown size={14} className="text-muted-foreground" />
        </button>

        <nav className="flex flex-col gap-0.5 px-3 py-2">
          {NAV.map((item) => {
            const isActive = item.key === active;
            const Icon = item.icon;
            const to = item.key === 'tokens' ? '/o/$org/settings/tokens' : `/o/$org/${item.key}`;
            return (
              <Link
                key={item.key}
                to={to}
                params={{ org: org.slug }}
                aria-current={isActive ? 'page' : undefined}
                className={
                  isActive
                    ? 'flex h-9 items-center gap-2.5 rounded-md bg-background px-3 font-semibold text-foreground shadow-[inset_2px_0_0_var(--px-accent)]'
                    : 'flex h-9 items-center gap-2.5 rounded-md px-3 font-medium text-muted-foreground hover:text-foreground'
                }
              >
                <Icon size={16} />
                <span className="flex-1">{item.label}</span>
              </Link>
            );
          })}
        </nav>

        <div className="mt-auto flex items-center gap-2.5 border-t border-border px-4 py-3.5">
          <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-accent-dim text-[11px] font-bold text-background">
            {initials(user.displayName)}
          </span>
          <span className="flex min-w-0 flex-1 flex-col">
            <span className="truncate text-xs font-semibold">{user.displayName}</span>
            <span className="truncate text-[11px] text-muted-foreground">{user.subtitle}</span>
          </span>
          <MoreHorizontal size={16} className="shrink-0 text-muted-foreground" />
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center gap-4 border-b border-border px-6">
          <div className="flex items-center gap-2 text-[13px] text-muted-foreground">{crumbs}</div>
          <div className="ml-auto flex h-[34px] w-[300px] items-center gap-2.5 rounded-md border border-border bg-card px-3 text-muted-foreground">
            <Search size={15} />
            <span className="flex-1 truncate text-[13px]">Search hosts…</span>
            <span className="rounded border border-border px-1.5 py-px font-mono text-[11px]">⌘K</span>
          </div>
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
