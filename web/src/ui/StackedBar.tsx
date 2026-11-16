/** StackedBarPart is one segment, already worded by the caller. */
export interface StackedBarPart {
  key: string;
  label: string;
  /** The segment's size; its share of the sum is its width. */
  value: number;
  /** The value as the legend prints it ("12 ms"). */
  display: string;
  /** The part's shade, 0 (darkest) to 4, fixed by what the part is so one
   * kind of part keeps its colour whichever others are present; its
   * position when absent. */
  shade?: number;
}

/** Shades of the accent, darkest first: the parts are phases in order, so
 * one hue stepping lighter reads as a sequence without a legend colour
 * that means a state (ok, warn, crit). */
const SHADES = [100, 78, 58, 42, 28];

function shade(p: StackedBarPart, i: number): string {
  const pct = SHADES[(p.shade ?? i) % SHADES.length];
  return `color-mix(in srgb, var(--px-accent) ${pct}%, var(--px-surface))`;
}

/**
 * StackedBar is one horizontal bar split into parts by size, with a legend
 * naming each part and its value — the http check's timing breakdown on
 * the host page (backlog 3.13b). Presentational: the feature picks and
 * words the parts.
 */
export function StackedBar({ label, parts }: { label: string; parts: readonly StackedBarPart[] }) {
  const total = parts.reduce((sum, p) => sum + Math.max(0, p.value), 0);
  return (
    <figure aria-label={label} className="m-0 flex flex-col gap-1.5">
      <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-[var(--px-border)]" aria-hidden="true">
        {total > 0 &&
          parts.map((p, i) => (
            <span
              key={p.key}
              data-testid="stacked-bar-segment"
              title={`${p.label}: ${p.display}`}
              className="h-full"
              style={{ width: `${(Math.max(0, p.value) / total) * 100}%`, backgroundColor: shade(p, i) }}
            />
          ))}
      </div>
      <ul className="m-0 flex list-none flex-wrap gap-x-4 gap-y-1 p-0 text-[11px]">
        {parts.map((p, i) => (
          <li key={p.key} className="flex items-center gap-1.5">
            <span className="h-2 w-2 rounded-sm" style={{ backgroundColor: shade(p, i) }} aria-hidden="true" />
            <span className="text-muted-foreground">{p.label}</span>
            <span className="font-mono tabular-nums text-foreground">{p.display}</span>
          </li>
        ))}
      </ul>
    </figure>
  );
}
