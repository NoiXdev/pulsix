import { useSyncExternalStore } from 'react';
import { Link, useNavigate, useRouter, useRouterState, type LinkProps } from '@tanstack/react-router';
import { Select } from './Select.tsx';

export interface SideNavItem {
  to: LinkProps['to'];
  params?: LinkProps['params'];
  label: string;
  /** A short marker after the label, such as a count. */
  badge?: string | number;
}

export interface SideNavGroup {
  label: string;
  items: SideNavItem[];
}

/** Below this width the grouped list gives way to a Select. */
const NARROW_QUERY = '(max-width: 899px)';

function subscribeNarrow(onChange: () => void) {
  const mql = window.matchMedia(NARROW_QUERY);
  mql.addEventListener('change', onChange);
  return () => mql.removeEventListener('change', onChange);
}

function useNarrow(): boolean {
  return useSyncExternalStore(
    subscribeNarrow,
    () => window.matchMedia(NARROW_QUERY).matches,
    () => false,
  );
}

/**
 * SideNav is a grouped list of links down the left of a section: a small
 * heading per group, one link per page, the current page marked with
 * `aria-current="page"`. Below 900px the list collapses into a Select that
 * navigates, so the section stays reachable without a column of links
 * pushing the page down. Presentational: the caller decides which groups
 * and items exist.
 */
export function SideNav({ groups, label }: { groups: SideNavGroup[]; label: string }) {
  const narrow = useNarrow();
  return narrow ? <NarrowNav groups={groups} label={label} /> : <WideNav groups={groups} label={label} />;
}

function WideNav({ groups, label }: { groups: SideNavGroup[]; label: string }) {
  return (
    <nav aria-label={label} className="flex flex-col gap-4">
      {groups.map((group) => (
        <div key={group.label} className="flex flex-col gap-0.5">
          <span className="px-3 pb-1 text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
            {group.label}
          </span>
          {group.items.map((item) => (
            <Link
              key={String(item.to)}
              to={item.to}
              params={item.params}
              className="flex h-8 items-center gap-2 rounded-md px-3 text-[13px] font-medium text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
              activeProps={{
                className: 'bg-card font-semibold text-foreground shadow-[inset_2px_0_0_var(--px-accent)]',
              }}
            >
              <span className="flex-1">{item.label}</span>
              {item.badge !== undefined && (
                <span className="rounded-full bg-background px-2 py-px font-mono text-[11px] text-muted-foreground">
                  {item.badge}
                </span>
              )}
            </Link>
          ))}
        </div>
      ))}
    </nav>
  );
}

function NarrowNav({ groups, label }: { groups: SideNavGroup[]; label: string }) {
  const router = useRouter();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });

  const entries = groups.flatMap((group) =>
    group.items.map((item) => ({
      item,
      group: group.label,
      path: router.buildLocation({ to: item.to, params: item.params }).pathname,
    })),
  );
  // The wide list marks an item active by prefix, so a nested page such as
  // /settings/shared/repos/123 keeps its parent item current. The longest
  // prefix wins, so an item nested under another's path is not shadowed.
  const current = entries
    .filter((e) => pathname === e.path || pathname.startsWith(`${e.path.replace(/\/$/, '')}/`))
    .sort((a, b) => b.path.length - a.path.length)[0];

  const onChange = (path: string) => {
    const target = entries.find((e) => e.path === path);
    if (target) void navigate({ to: target.item.to, params: target.item.params });
  };

  return (
    <nav aria-label={label}>
      <Select
        aria-label={label}
        options={entries.map((e) => ({ value: e.path, label: e.item.label, hint: e.group }))}
        value={current?.path ?? ''}
        onChange={onChange}
      />
    </nav>
  );
}
