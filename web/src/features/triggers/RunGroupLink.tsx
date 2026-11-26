import type { MouseEvent } from 'react';
import { useRouter } from '@tanstack/react-router';
import { firstJobOfRunGroup } from '../../api/hooks/useRunRequests.ts';
import { shortId } from '../runs/status.ts';

/**
 * RunGroupLink names a run by its run group (a delivery log names the run a
 * delivery started or collapsed into by its group) and opens the group's
 * first job, or the runs list while none is listed yet. It works inside a
 * router and in a dialog rendered without one.
 */
export function RunGroupLink({ org, group }: { org: string; group: string }) {
  const router = useRouter({ warn: false }) as ReturnType<typeof useRouter> | undefined;
  const list = `/o/${encodeURIComponent(org)}/runs`;

  async function open(e: MouseEvent<HTMLAnchorElement>) {
    e.preventDefault();
    const job = await firstJobOfRunGroup(org, group).catch(() => undefined);
    const path = job ? `${list}/${encodeURIComponent(job)}` : list;
    if (router) router.history.push(path);
    else window.location.assign(path);
  }

  return (
    <a href={list} className="font-mono text-primary hover:underline" onClick={(e) => void open(e)}>
      {shortId(group)}
    </a>
  );
}
