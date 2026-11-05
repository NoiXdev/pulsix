import type { ReactNode } from 'react';
import { cn } from '../lib/utils.ts';
import { useT } from '../i18n/useT.ts';
import { roleLabel } from '../i18n/roleLabel.ts';

export type Tone = 'ok' | 'warn' | 'crit' | 'unknown' | 'accent' | 'maintenance';

const TONE_VAR: Record<Tone, string> = {
  ok: 'var(--px-ok)',
  warn: 'var(--px-warn)',
  crit: 'var(--px-crit)',
  unknown: 'var(--px-unknown)',
  accent: 'var(--px-accent)',
  maintenance: 'var(--px-maintenance)',
};

/** Pill — design/mockups/build.mjs's `pill()` generator: a coloured dot
 * plus label, background/border at ~10%/20% opacity of the tone colour.
 * Tones map 1:1 onto the state colours brand/README.md reserves for
 * monitoring states (ok/warn/crit/unknown), plus two explicit non-state
 * exceptions: `accent`, for a badge like "invited" that still wants the
 * pill shape, and `maintenance` (the mockup's violet), for a maintenance
 * window's status — never `warn`, which is reserved for a degraded
 * monitoring state and would visually conflate "planned" with "wrong". */
export function Pill({ tone, children }: { tone: Tone; children: ReactNode }) {
  const color = TONE_VAR[tone];
  return (
    <span
      className="inline-flex items-center gap-1.5 rounded-full border px-2.5 py-0.5 text-[11px] font-semibold"
      style={{ color, backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`, borderColor: `color-mix(in srgb, ${color} 33%, transparent)` }}
    >
      <span className="h-1.5 w-1.5 rounded-full" style={{ backgroundColor: color }} />
      {children}
    </span>
  );
}

const ROLE_COLOR: Record<string, string> = {
  owner: 'var(--px-accent)',
  admin: '#A78BFA',
  operator: 'var(--px-ok)',
  viewer: 'var(--px-unknown)',
};

/** RoleBadge — design's `roleBadge()` generator: same pill shape as Pill,
 * but keyed by role name (owner/admin/operator/viewer) instead of a
 * monitoring tone, since a role is not a state. */
export function RoleBadge({ role }: { role: string }) {
  const t = useT();
  const color = ROLE_COLOR[role] ?? 'var(--px-unknown)';
  return (
    <span
      className="inline-flex items-center rounded-full border px-2.5 py-0.5 text-[11px] font-semibold capitalize"
      style={{ color, backgroundColor: `color-mix(in srgb, ${color} 10%, transparent)`, borderColor: `color-mix(in srgb, ${color} 33%, transparent)` }}
    >
      {roleLabel(t, role)}
    </span>
  );
}

export function Tag({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded border border-border bg-background px-2 py-0.5 font-mono text-[11px] text-muted-foreground',
        className,
      )}
    >
      {children}
    </span>
  );
}
