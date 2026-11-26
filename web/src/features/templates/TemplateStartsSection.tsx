import type { ReactNode } from 'react';
import { Pill } from '../../ui/badge.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { ListRow, ListRowName } from '../../ui/list-row.tsx';
import { Card } from '../../ui/table.tsx';
import type { Schedule, Template, Trigger } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { relative } from '../schedules/simple.ts';
import { ScheduleStatusPill } from '../schedules/ScheduleStatusPill.tsx';
import { SectionAction } from '../hosts/HostOverviewCards.tsx';
import { useNow } from '../runs/runRequestStatus.ts';
import type { TemplateTab } from './templateTabs.ts';

const SCHEDULE_COLUMNS = 'minmax(0,1fr) minmax(0,1fr) 110px 90px';
const FOLLOW_UP_COLUMNS = '24px minmax(0,1fr)';

interface TemplateStartsSectionProps {
  org: string;
  template: Template;
  /** The template's schedules and webhook triggers; undefined while read. */
  schedules: readonly Schedule[] | undefined;
  triggers: readonly Trigger[] | undefined;
  onTab: (tab: TemplateTab) => void;
}

/**
 * TemplateStartsSection is the Overview's "What it starts": the schedules
 * and webhook triggers that start the template, each with its state (an
 * assistant's change pausing one says so), and the follow-ups a successful
 * run starts, in order. A schedule opens its editor, a follow-up its own
 * template page; the Schedules and Triggers tabs are one click away.
 */
export function TemplateStartsSection({ org, template, schedules, triggers, onTab }: TemplateStartsSectionProps) {
  const t = useT();
  const o = t.templates.page.overview;
  const now = useNow();
  const followUps = [...(template.follow_ups ?? [])].sort((a, b) => a.position - b.position);

  return (
    <Section id="template-starts" title={o.starts}>
      <Group title={o.schedules} action={<SectionAction onClick={() => onTab('schedules')}>{o.toSchedules}</SectionAction>}>
        {!schedules ? (
          <Muted>{t.common.loading}</Muted>
        ) : schedules.length === 0 ? (
          <Muted>{o.noSchedules}</Muted>
        ) : (
          <Card>
            {schedules.map((s) => (
              <ListRow key={s.id} to="/o/$org/schedules" params={{ org }} search={{ id: s.id, template: template.id }} columns={SCHEDULE_COLUMNS} label={s.name}>
                <ListRowName className="truncate text-[13px] font-medium">{s.name}</ListRowName>
                <span className="truncate font-mono text-xs text-muted-foreground" title={s.description || undefined}>
                  {s.cron}
                </span>
                <span>
                  <ScheduleStatusPill schedule={s} />
                </span>
                <span className="text-right text-xs text-muted-foreground">{s.paused ? '—' : relative(s.next_run_at, now, t)}</span>
              </ListRow>
            ))}
          </Card>
        )}
      </Group>
      <Group title={o.triggers} action={<SectionAction onClick={() => onTab('triggers')}>{o.toTriggers}</SectionAction>}>
        {!triggers ? (
          <Muted>{t.common.loading}</Muted>
        ) : triggers.length === 0 ? (
          <Muted>{o.noTriggers}</Muted>
        ) : (
          <Card>
            <ul aria-label={o.triggers}>
              {triggers.map((tr) => (
                <li key={tr.id} className="flex items-center justify-between gap-3 border-b border-border px-4 py-2.5 text-[13px] last:border-b-0">
                  <span className="min-w-0 truncate font-medium">{tr.name}</span>
                  {tr.disabled_reason ? (
                    <Pill tone="warn">{t.schedules.needsReview}</Pill>
                  ) : tr.enabled ? (
                    <Pill tone="ok">{t.triggers.enabled}</Pill>
                  ) : (
                    <Pill tone="unknown">{t.triggers.disabled}</Pill>
                  )}
                </li>
              ))}
            </ul>
          </Card>
        )}
      </Group>
      <Group title={o.followUps}>
        {followUps.length === 0 ? (
          <Muted>{o.noFollowUps}</Muted>
        ) : (
          <Card>
            {followUps.map((f, i) => (
              <ListRow key={f.template_id} to="/o/$org/templates/$id" params={{ org, id: f.template_id }} search={{ tab: undefined }} columns={FOLLOW_UP_COLUMNS} label={f.template_name}>
                <span className="text-xs text-muted-foreground">{i + 1}.</span>
                <ListRowName className="truncate font-mono text-[13px]">{f.template_name}</ListRowName>
              </ListRow>
            ))}
          </Card>
        )}
      </Group>
    </Section>
  );
}

function Group({ title, action, children }: { title: string; action?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-3">
        <h3 className="flex-1 text-xs font-semibold text-muted-foreground">{title}</h3>
        {action}
      </div>
      {children}
    </div>
  );
}

function Muted({ children }: { children: ReactNode }) {
  return <p className="text-[13px] text-muted-foreground">{children}</p>;
}
