import type { MaintenanceWindow } from '../../api/types.ts';

const pad = (n: number) => String(n).padStart(2, '0');

/** toLocalInput renders an instant for a datetime-local input (the
 * browser's own time zone), the inverse of fromLocalInput. */
export function toLocalInput(iso?: string | null): string {
  if (!iso) return '';
  const d = new Date(iso);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** fromLocalInput reads a datetime-local value as the browser's local
 * time and returns the instant as ISO 8601 (UTC). */
export function fromLocalInput(value: string): string {
  return new Date(value).toISOString();
}

/** whenLabel says when a window applies, in words. */
export function whenLabel(w: MaintenanceWindow): string {
  if (w.cron) {
    return `${w.description || w.cron} · for ${w.duration ?? ''} · ${w.timezone ?? ''}`;
  }
  const fmt = (iso?: string | null) => (iso ? new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }) : '');
  return `${fmt(w.starts_at)} – ${fmt(w.ends_at)}`;
}

/** targetLabel names what a window covers. */
export function targetLabel(w: MaintenanceWindow): string {
  if (w.target_kind === 'org') return 'Whole organization';
  return `${w.target_kind === 'host' ? 'Host' : 'Group'} ${w.target_name ?? ''}`.trim();
}
