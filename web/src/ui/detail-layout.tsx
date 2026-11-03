import { useEffect, useState, type ReactNode } from 'react';
import { cn } from '../lib/utils.ts';

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
 */
function useCurrentSection(ids: string[]): string | undefined {
  const [current, setCurrent] = useState<string | undefined>(ids[0]);
  const key = ids.join('\n');

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

  return current;
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
  children,
}: {
  sidebar: ReactNode;
  nav: SectionNavItem[];
  children: ReactNode;
}) {
  const current = useCurrentSection(nav.map((n) => n.id));

  return (
    <div className="grid grid-cols-1 items-start gap-6 min-[1100px]:grid-cols-[300px_minmax(0,1fr)]">
      <aside className="flex min-w-0 flex-col gap-4 min-[1100px]:sticky min-[1100px]:top-0 min-[1100px]:max-h-[calc(100vh-8rem)] min-[1100px]:overflow-y-auto">
        {sidebar}
        {nav.length > 0 && (
          <nav className="flex flex-col gap-0.5">
            {nav.map((item) => {
              const active = item.id === current;
              return (
                <a
                  key={item.id}
                  href={`#${item.id}`}
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
