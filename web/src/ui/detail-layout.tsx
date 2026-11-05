import { useEffect, useState, type ReactNode } from 'react';
import { cn } from '../lib/utils.ts';
import type { Tone } from './badge.tsx';

export interface SectionNavItem {
  id: string;
  label: string;
  count?: number;
  tone?: 'warn' | 'crit';
}

const TONE_VAR = { warn: 'var(--px-warn)', crit: 'var(--px-crit)' } as const;

/** A strip across the upper part of the viewport: a section counts as the
 * current one while it overlaps it, so the highlight changes as a heading
 * scrolls up under the top bar rather than when the whole section enters. */
const OBSERVER_MARGIN = '-10% 0px -70% 0px';

/**
 * useCurrentSection reports which section id is current. Without an
 * IntersectionObserver (jsdom, very old browsers) the first item stays
 * current, which is also what the page shows before anything has scrolled.
 * Of the sections that intersect the observed strip the topmost one in nav
 * order wins; when none does (a gap between sections) the previous one stays.
 * A current id that has left `ids` falls back to the first one.
 */
function useCurrentSection(ids: string[]): { current: string | undefined; markHandled: (id: string) => void } {
  const [current, setCurrent] = useState<string | undefined>(ids[0]);
  const key = ids.join('\n');
  // The URL hash may name a section that only exists once an async read is
  // done (`/groups/x#members`), so a hash is acted on when its section enters
  // the nav, once: the current section is seeded while rendering and the
  // scroll follows in an effect. A hash the user produced on this page (a nav
  // click, back/forward) is marked handled instead, because the browser has
  // already scrolled there; acting on it at the next render would pull the
  // page back from wherever the user has scrolled since.
  const hash = typeof window === 'undefined' ? '' : window.location.hash.slice(1);
  const [handled, setHandled] = useState<string[]>([]);
  const [deepLink, setDeepLink] = useState<string | null>(null);
  if (hash !== '' && !handled.includes(hash) && ids.includes(hash)) {
    setHandled([...handled, hash]);
    setCurrent(hash);
    setDeepLink(hash);
  }

  const markHandled = (id: string) => setHandled((prev) => (prev.includes(id) ? prev : [...prev, id]));
  useEffect(() => {
    const onHashChange = () => setHandled((prev) => (prev.includes(window.location.hash.slice(1)) ? prev : [...prev, window.location.hash.slice(1)]));
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  useEffect(() => {
    if (deepLink !== null) document.getElementById(deepLink)?.scrollIntoView({ block: 'start' });
  }, [deepLink]);

  useEffect(() => {
    if (typeof IntersectionObserver === 'undefined') return;
    const order = key === '' ? [] : key.split('\n');
    const visible = new Set<string>();
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const id = (entry.target as HTMLElement).id;
          if (entry.isIntersecting) visible.add(id);
          else visible.delete(id);
        }
        const top = order.find((id) => visible.has(id));
        if (top) setCurrent(top);
      },
      { rootMargin: OBSERVER_MARGIN },
    );
    for (const id of order) {
      const el = document.getElementById(id);
      if (el) observer.observe(el);
    }
    return () => observer.disconnect();
  }, [key]);

  // The nav can change under a live page (sections come and go with what the
  // host has); a seeded or observed id that is no longer in it falls back to
  // the first item instead of leaving nothing highlighted.
  return { current: current !== undefined && ids.includes(current) ? current : ids[0], markHandled };
}

/**
 * DetailLayout is the frame of a detail page: a sidebar of about 300px that
 * sticks while the sections scroll, a section nav inside it, and the section
 * column beside it. Below 1100px the sidebar stacks above the sections and
 * stops sticking. Presentational: the page hands it the sidebar body and the
 * nav entries, and renders <Section>s as children whose ids match the nav.
 */
export function DetailLayout({
  sidebar,
  nav,
  navLabel,
  children,
}: {
  sidebar: ReactNode;
  nav: SectionNavItem[];
  /** Accessible name of the section nav: the shell has a nav of its own. */
  navLabel: string;
  children: ReactNode;
}) {
  const { current, markHandled } = useCurrentSection(nav.map((n) => n.id));

  return (
    <div className="grid grid-cols-1 items-start gap-6 min-[1100px]:grid-cols-[300px_minmax(0,1fr)]">
      {/* top-6 matches the shell's <main> padding: <main> is the scroller,
          and sticking at 0 would pin the sidebar flush to its top edge. */}
      <aside className="flex min-w-0 flex-col gap-4 min-[1100px]:sticky min-[1100px]:top-6 min-[1100px]:max-h-[calc(100vh-8rem)] min-[1100px]:overflow-y-auto">
        {sidebar}
        {nav.length > 0 && (
          <nav aria-label={navLabel} className="flex flex-col gap-0.5">
            {nav.map((item) => {
              const active = item.id === current;
              return (
                <a
                  key={item.id}
                  href={`#${item.id}`}
                  onClick={() => markHandled(item.id)}
                  aria-current={active ? 'location' : undefined}
                  className={cn(
                    'flex h-8 items-center gap-2 rounded-md px-3 text-[13px] hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
                    active
                      ? 'bg-card font-semibold text-foreground shadow-[inset_2px_0_0_var(--px-accent)]'
                      : 'font-medium text-muted-foreground',
                  )}
                >
                  <span className="flex-1">{item.label}</span>
                  {item.count !== undefined && (
                    <span
                      className="rounded-full bg-background px-2 py-px font-mono text-[11px] text-muted-foreground"
                      style={item.tone ? { color: TONE_VAR[item.tone] } : undefined}
                    >
                      {item.count}
                    </span>
                  )}
                </a>
              );
            })}
          </nav>
        )}
      </aside>
      <div className="flex min-w-0 flex-col gap-8">{children}</div>
    </div>
  );
}

/** One titled block of a detail page. `id` is the anchor the nav links to;
 * `actions` sit on the right of the heading. */
export function Section({
  id,
  title,
  actions,
  children,
}: {
  id: string;
  title: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const headingId = `${id}-heading`;
  return (
    <section id={id} aria-labelledby={headingId} className="flex scroll-mt-6 flex-col gap-3">
      <div className="flex min-h-8 items-center gap-3">
        <h2 id={headingId} className="flex-1 text-sm font-semibold">
          {title}
        </h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

const DOT: Record<Tone, string> = {
  ok: 'var(--px-ok)',
  warn: 'var(--px-warn)',
  crit: 'var(--px-crit)',
  unknown: 'var(--px-unknown)',
  accent: 'var(--px-accent)',
  maintenance: 'var(--px-maintenance)',
};

/** SidebarHeading is the top of a detail page's sidebar: the page's `h1`
 * (monospaced for an identifier) with an optional status dot, the state pill
 * below it and `children` as a muted sub-line. */
export function SidebarHeading({
  title,
  mono,
  dot,
  pill,
  children,
}: {
  title: string;
  mono?: boolean;
  dot?: { tone: Tone; label: string };
  pill?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="flex flex-col items-start gap-1.5">
      <div className="flex min-w-0 items-center gap-2.5">
        {dot && <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ background: DOT[dot.tone] }} title={dot.label} />}
        <h1 className={cn('min-w-0 text-xl font-bold break-all', mono && 'font-mono')}>{title}</h1>
      </div>
      {pill}
      {children && <div className="text-[13px] text-muted-foreground">{children}</div>}
    </div>
  );
}

/** SidebarFacts is a definition list of label/value pairs for a detail page's
 * sidebar. */
export function SidebarFacts({ items }: { items: { label: string; value: ReactNode }[] }) {
  return (
    <dl className="flex flex-col gap-2.5 rounded-[10px] border border-border bg-card px-4 py-3 text-[13px]">
      {items.map((item) => (
        <div key={item.label} className="flex flex-col gap-0.5">
          <dt className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">{item.label}</dt>
          <dd className="min-w-0 break-words">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** DisabledHint stands in for a section whose feature is unavailable, and
 * says why and, when there is one, what to do about it. */
export function DisabledHint({ title, body, action }: { title: string; body: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex shrink-0 flex-col items-start gap-1.5 rounded-[10px] border border-dashed border-border bg-card px-4 py-3.5">
      <span className="text-[13px] font-semibold">{title}</span>
      <span className="text-[13px] text-muted-foreground">{body}</span>
      {action && <div className="mt-1">{action}</div>}
    </div>
  );
}
