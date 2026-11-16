import { Link } from '@tanstack/react-router';
import { Pill, Tag } from '../../ui/badge.tsx';
import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import type { JobDetail } from '../../api/types.ts';
import { chainTemplateName } from './chainTemplateName.ts';
import { useT } from '../../i18n/useT.ts';

type FollowUp = NonNullable<NonNullable<JobDetail['chain']>['follow_ups']>[number];

/** RunChainSection lists the run's follow-ups in start order: the template,
 * how many hosts it runs on, its state and the reason the server gave. A
 * started follow-up links to its run. The badges use the accent or the
 * neutral tag, never a status colour (those belong to hosts and checks). */
export function RunChainSection({ org, followUps }: { org: string; followUps: FollowUp[] }) {
  const t = useT();
  const c = t.runs.detail.chain;

  return (
    <Section id="chain" title={c.title}>
      <Card>
        <ul>
          {followUps.map((f) => {
            const hosts = f.hosts ?? [];
            const label = c.state[f.state];
            const name = chainTemplateName(f.template_name, t);
            const active = f.state === 'pending' || f.state === 'due' || f.state === 'started';
            return (
              <li key={f.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-4 py-2.5 last:border-b-0">
                {active ? <Pill tone="accent">{label}</Pill> : <Tag>{label}</Tag>}
                <span className="font-mono text-[13px]">{name}</span>
                <span className="text-xs text-muted-foreground">{c.hosts(hosts.length)}</span>
                {f.reason && <span className="min-w-0 flex-1 text-xs text-muted-foreground">{f.reason}</span>}
                {f.state === 'started' && f.child_first_job_id && (
                  <Link
                    to="/o/$org/runs/$id"
                    params={{ org, id: f.child_first_job_id }}
                    className="ml-auto text-xs hover:text-primary"
                    aria-label={`${c.viewRun}: ${name}`}
                  >
                    {c.viewRun}
                  </Link>
                )}
              </li>
            );
          })}
        </ul>
      </Card>
    </Section>
  );
}
