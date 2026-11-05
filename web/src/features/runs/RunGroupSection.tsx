import { Link } from '@tanstack/react-router';
import type { JobDetail } from '../../api/types.ts';
import { shortId } from './status.ts';
import { useT } from '../../i18n/useT.ts';

/** RunGroupSection lists the other jobs the same trigger started (one per
 * zone involved), each linked to its run. */
export function RunGroupSection({ org, siblings }: { org: string; siblings: NonNullable<JobDetail['group']> }) {
  const t = useT();
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-muted-foreground" aria-label={t.runs.detail.runGroup}>
      <span>{t.runs.detail.sameTrigger}</span>
      {siblings.map((s) => (
        <Link key={s.id} to="/o/$org/runs/$id" params={{ org, id: s.id }} className="flex items-center gap-1.5 font-mono text-[13px] hover:text-primary">
          <span className="h-1.5 w-1.5 rounded-full" style={{ background: statusColor(s.status) }} />
          {shortId(s.id)}
        </Link>
      ))}
    </div>
  );
}

function statusColor(status: string): string {
  switch (status) {
    case 'succeeded':
      return 'var(--px-ok)';
    case 'failed':
    case 'error':
      return 'var(--px-crit)';
    case 'running':
      return 'var(--px-accent)';
    case 'cancelling':
      return 'var(--px-warn)';
    default:
      return 'var(--px-unknown)';
  }
}
