import { useEffect, useState } from 'react';
import type { Tone } from '../../ui/badge.tsx';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';

/** requestTone colours a run request's status: a pending one waits for
 * someone (the accent), one waiting for its sync is on its way there, an
 * approved one ran, a failed one (its sync failed) warns, the others ended
 * without a run. */
export function requestTone(status: RunRequest['status']): Tone {
  if (status === 'pending' || status === 'waiting_for_sync') return 'accent';
  if (status === 'approved') return 'ok';
  if (status === 'failed') return 'warn';
  return 'unknown';
}

/** sortRequests puts the pending requests first, the soonest to expire on
 * top, then those waiting for their sync, oldest first, then the rest,
 * newest first. */
export function sortRequests(requests: readonly RunRequest[]): RunRequest[] {
  const pending = requests
    .filter((r) => r.status === 'pending')
    .sort((a, b) => (a.expires_at ?? '').localeCompare(b.expires_at ?? ''));
  const waiting = requests.filter((r) => r.status === 'waiting_for_sync').sort((a, b) => a.created_at.localeCompare(b.created_at));
  const rest = requests
    .filter((r) => r.status !== 'pending' && r.status !== 'waiting_for_sync')
    .sort((a, b) => b.created_at.localeCompare(a.created_at));
  return [...pending, ...waiting, ...rest];
}

/** useNow is the current time, refreshed every `intervalMs`, so an expiry
 * countdown moves while the page is open. */
export function useNow(intervalMs = 30_000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const timer = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
