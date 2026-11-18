import { useRef, type KeyboardEvent } from 'react';
import { cn } from '../lib/utils.ts';

export interface PageTabItem<T extends string> {
  id: T;
  label: string;
  count?: number;
  /** Highlights the count (open alerts). */
  tone?: 'warn' | 'crit';
}

const TONE_VAR = { warn: 'var(--px-warn)', crit: 'var(--px-crit)' } as const;

/**
 * PageTabs is a detail page's tab strip (the WAI-ARIA tabs pattern): one
 * button per tab, the open one selected and the only one in the Tab order;
 * the arrow keys, Home and End move between them and open the one they
 * land on (`via` tells the page which, so a click may add a history entry
 * and keyboard roving does not). The page owns which tab is open (it lives
 * in the URL) and the tab panel, whose id is `panelId`; `tabId` names
 * each tab's button, which labels that panel while its tab is open. A page
 * that keeps a panel mounted while another tab is open (a form whose
 * unsaved input must survive a tab switch) has one panel per tab, and
 * `panelId` is then a function naming each tab's own.
 */
export function PageTabs<T extends string>({
  label,
  tabs,
  active,
  onSelect,
  tabId,
  panelId,
}: {
  label: string;
  tabs: PageTabItem<T>[];
  active: T;
  onSelect: (tab: T, via: 'click' | 'key') => void;
  tabId: (id: T) => string;
  panelId: string | ((id: T) => string);
}) {
  const refs = useRef(new Map<T, HTMLButtonElement>());

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const index = tabs.findIndex((tab) => tab.id === active);
    const next: Record<string, number> = { ArrowRight: index + 1, ArrowLeft: index - 1, Home: 0, End: tabs.length - 1 };
    if (!(e.key in next)) return;
    e.preventDefault();
    const target = tabs[(next[e.key] + tabs.length) % tabs.length];
    onSelect(target.id, 'key');
    refs.current.get(target.id)?.focus();
  }

  return (
    <div role="tablist" aria-label={label} onKeyDown={onKeyDown} className="flex gap-5 overflow-x-auto border-b border-border">
      {tabs.map((tab) => {
        const selected = tab.id === active;
        return (
          <button
            key={tab.id}
            ref={(el) => {
              if (el) refs.current.set(tab.id, el);
              else refs.current.delete(tab.id);
            }}
            type="button"
            role="tab"
            id={tabId(tab.id)}
            aria-selected={selected}
            aria-controls={typeof panelId === 'string' ? panelId : panelId(tab.id)}
            tabIndex={selected ? 0 : -1}
            onClick={() => onSelect(tab.id, 'click')}
            className={cn(
              '-mb-px flex shrink-0 items-center gap-1.5 border-b-2 py-2.5 text-[13px] whitespace-nowrap focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary',
              selected ? 'border-primary font-semibold text-foreground' : 'border-transparent font-medium text-muted-foreground hover:text-foreground',
            )}
          >
            <span>{tab.label}</span>
            {tab.count !== undefined && (
              <span
                data-tone={tab.tone}
                className="rounded-full bg-card px-2 py-px font-mono text-[11px] text-muted-foreground"
                style={tab.tone ? { color: TONE_VAR[tab.tone] } : undefined}
              >
                {tab.count}
              </span>
            )}
          </button>
        );
      })}
    </div>
  );
}
