import type { ReactNode } from 'react';
import { Pill } from '../../ui/badge.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { Card } from '../../ui/table.tsx';
import { useJobs } from '../../api/hooks/useJobs.ts';
import { useFormat } from '../../i18n/format.ts';
import { useT } from '../../i18n/useT.ts';
import { kindLabel, shortId, statusLabel, statusTone } from '../runs/status.ts';
import { triggerLabel } from '../runs/trigger.ts';

/** How many runs the section shows by default. */
const RECENT = 5;

const COLUMNS = '90px minmax(0,1fr) 110px 120px';

/**
 * TemplateRunsSection is the template page's "Recent runs": the newest
 * runs of the template (the jobs list's own `template_id` filter), each a
 * row to its run with its kind (a dry run badged), status, trigger and age.
 * `actions` sit beside the title (the Overview's way to the Runs tab).
 */
export function TemplateRunsSection({ org, templateId, limit = RECENT, actions }: { org: string; templateId: string; limit?: number; actions?: ReactNode }) {
  const t = useT();
  const fmt = useFormat();
  const o = t.templates.page.overview;
  const jobs = useJobs(org, { template: templateId, limit });
  const rows = (jobs.data?.pages[0]?.jobs ?? []).slice(0, limit);

  return (
    <Section id="template-runs" title={o.runs} actions={actions}>
      {jobs.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {jobs.error instanceof Error ? jobs.error.message : o.runsFailed}
        </p>
      ) : !jobs.data ? (
        <p className="text-[13px] text-muted-foreground">{t.common.loading}</p>
      ) : rows.length === 0 ? (
        <p className="text-[13px] text-muted-foreground">{o.noRuns}</p>
      ) : (
        <Card>
          {rows.map((j) => (
            <ListRow key={j.id} to="/o/$org/runs/$id" params={{ org, id: j.id }} columns={COLUMNS} label={shortId(j.id)}>
              <ListRowName className="font-mono text-[13px]">{shortId(j.id)}</ListRowName>
              <span className="flex min-w-0 items-center gap-1.5 text-[13px]">
                <span className="truncate">{triggerLabel(j.trigger, t)}</span>
                {j.kind !== 'run' && <span className="truncate text-muted-foreground">{kindLabel(j.kind, t)}</span>}
                {j.check_mode && <Pill tone="accent">{t.runs.dryRun.badge}</Pill>}
              </span>
              <span>
                <Pill tone={statusTone(j.status, j.withdrawn_at_claim)}>{statusLabel(j.status, t, j.withdrawn_at_claim)}</Pill>
              </span>
              <span className="text-right text-xs text-muted-foreground" title={fmt.dateTime(j.created_at)}>
                {fmt.relative(j.created_at)}
              </span>
            </ListRow>
          ))}
        </Card>
      )}
    </Section>
  );
}
