/** EXPIRY_WARN_DAYS: certificates closer to expiry are highlighted (spec §4). */
export const EXPIRY_WARN_DAYS = 14;

/** daysLeft is the whole days until at, negative once expired. */
export function daysLeft(at: string, now: Date): number {
  return Math.floor((new Date(at).getTime() - now.getTime()) / 86_400_000);
}

/** expiryLabel renders a certificate expiry ("in 41 d", "expired"). */
export function expiryLabel(at: string | undefined | null, now: Date): string {
  if (!at) return '—';
  const d = daysLeft(at, now);
  if (d < 0) return 'expired';
  if (d === 0) return 'today';
  return `in ${d} d`;
}

/** seenLabel renders a heartbeat age ("just now", "5 min ago", "3 h ago", "2 d ago"). */
export function seenLabel(at: string | undefined | null, now: Date): string {
  if (!at) return 'never';
  const mins = Math.floor((now.getTime() - new Date(at).getTime()) / 60_000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins} min ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 48) return `${hours} h ago`;
  return `${Math.floor(hours / 24)} d ago`;
}
