import { cn } from '../lib/utils.ts';

export interface Tab {
  key: string;
  label: string;
}

export interface TabsProps {
  tabs: Tab[];
  active: string;
  onChange: (key: string) => void;
}

/** Tabs — design's `tab()` generator: an underline (accent, 2px) on the
 * active label, no background pill. */
export function Tabs({ tabs, active, onChange }: TabsProps) {
  return (
    <div role="tablist" className="flex gap-6 border-b border-border">
      {tabs.map((t) => (
        <button
          key={t.key}
          type="button"
          role="tab"
          aria-selected={t.key === active}
          onClick={() => onChange(t.key)}
          className={cn(
            'border-b-2 py-2.5 text-[13px] font-medium',
            t.key === active
              ? 'border-primary font-semibold text-foreground'
              : 'border-transparent text-muted-foreground',
          )}
        >
          {t.label}
        </button>
      ))}
    </div>
  );
}
