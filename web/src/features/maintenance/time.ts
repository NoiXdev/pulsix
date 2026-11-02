import type { Format } from '../../i18n/format.ts';
import type { Messages } from '../../i18n/types.ts';
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
export function whenLabel(w: MaintenanceWindow, t: Messages, fmt: Format): string {
  if (w.cron) {
    return t.maintenance.when.recurring(w.description || w.cron, w.duration ?? '', w.timezone ?? '');
  }
  const at = (iso?: string | null) => (iso ? fmt.dateTime(iso, { dateStyle: 'medium', timeStyle: 'short' }) : '');
  return `${at(w.starts_at)} – ${at(w.ends_at)}`;
}

/** targetLabel names what a window covers. */
export function targetLabel(w: MaintenanceWindow, t: Messages): string {
  if (w.target_kind === 'org') return t.maintenance.target.org;
  return `${w.target_kind === 'host' ? t.maintenance.target.host : t.maintenance.target.group} ${w.target_name ?? ''}`.trim();
}
