import { useEffect, useState } from 'react';

/** How long a dry run may sit queued before the page says why it might. */
export const QUEUED_HINT_MS = 10_000;

/** useQueuedLong says a job of the run has been queued for QUEUED_HINT_MS
 * or longer: a dry run no worker of its zone takes (only workers that
 * support check mode do). A `createdAt` that does not parse says nothing and
 * starts no timer. */
export function useQueuedLong(queued: boolean, createdAt: string): boolean {
  const due = Date.parse(createdAt) + QUEUED_HINT_MS;
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!queued || !Number.isFinite(due) || now >= due) return;
    const timer = setTimeout(() => setNow(Date.now()), due - now);
    return () => clearTimeout(timer);
  }, [queued, due, now]);
  return queued && Number.isFinite(due) && now >= due;
}
