import type { Messages } from '../../i18n/types.ts';

/** EXPIRY_WARN_DAYS: certificates closer to expiry are highlighted (spec §4). */
export const EXPIRY_WARN_DAYS = 14;

/** daysLeft is the whole days until at, negative once expired. */
export function daysLeft(at: string, now: Date): number {
  return Math.floor((new Date(at).getTime() - now.getTime()) / 86_400_000);
}

/** expiryLabel renders a certificate expiry ("in 41 d", "expired"). */
export function expiryLabel(at: string | undefined | null, now: Date, t: Messages): string {
  if (!at) return '—';
  const d = daysLeft(at, now);
  if (d < 0) return t.workers.time.expired;
  if (d === 0) return t.workers.time.today;
  return t.workers.time.inDays(d);
}

/** seenLabel renders a heartbeat age ("just now", "5 min ago", "3 h ago", "2 d ago"). */
export function seenLabel(at: string | undefined | null, now: Date, t: Messages): string {
  if (!at) return t.workers.time.never;
  const mins = Math.floor((now.getTime() - new Date(at).getTime()) / 60_000);
  if (mins < 1) return t.workers.time.justNow;
  if (mins < 60) return t.workers.time.minutesAgo(mins);
  const hours = Math.floor(mins / 60);
  if (hours < 48) return t.workers.time.hoursAgo(hours);
  return t.workers.time.daysAgo(Math.floor(hours / 24));
}
