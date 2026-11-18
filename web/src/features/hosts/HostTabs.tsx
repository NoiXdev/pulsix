import { useRef, type KeyboardEvent } from 'react';
import { cn } from '../../lib/utils.ts';
import { HOST_PANEL_ID, hostTabId, type HostTab } from './hostTabs.ts';

export interface HostTabItem {
  id: HostTab;
  label: string;
  count?: number;
  /** Highlights the count (open alerts). */
  tone?: 'warn' | 'crit';
}

const TONE_VAR = { warn: 'var(--px-warn)', crit: 'var(--px-crit)' } as const;

/**
 * HostTabs is the host page's tab strip (the WAI-ARIA tabs pattern): one
 * button per tab, the open one selected and the only one in the Tab order;
 * the arrow keys, Home and End move between them and open the one they
 * land on. The page owns which tab is open (it lives in the URL).
 */
export function HostTabs({ label, tabs, active, onSelect }: { label: string; tabs: HostTabItem[]; active: HostTab; onSelect: (tab: HostTab) => void }) {
  const refs = useRef(new Map<HostTab, HTMLButtonElement>());

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const index = tabs.findIndex((tab) => tab.id === active);
    const next: Record<string, number> = { ArrowRight: index + 1, ArrowLeft: index - 1, Home: 0, End: tabs.length - 1 };
    if (!(e.key in next)) return;
    e.preventDefault();
    const target = tabs[(next[e.key] + tabs.length) % tabs.length];
    onSelect(target.id);
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
            id={hostTabId(tab.id)}
            aria-selected={selected}
            aria-controls={HOST_PANEL_ID}
            tabIndex={selected ? 0 : -1}
            onClick={() => onSelect(tab.id)}
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
