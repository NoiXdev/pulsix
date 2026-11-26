import { useState } from 'react';
import { useNavigate } from '@tanstack/react-router';
import { Clock } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { Card } from '../../ui/table.tsx';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import type { Schedule, Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { useNow } from '../runs/runRequestStatus.ts';
import { ScheduleEditor } from '../schedules/ScheduleEditor.tsx';
import { ScheduleStatusPill } from '../schedules/ScheduleStatusPill.tsx';
import { relative } from '../schedules/simple.ts';
import { useScheduleActions } from '../schedules/useScheduleActions.ts';

const COLUMNS = 'minmax(0,1.1fr) 110px minmax(0,1fr) 130px minmax(0,1fr) 28px';

export interface TemplateSchedulesTabProps {
  org: string;
  template: Template;
  role: string;
  /** The template's schedules (the page reads them for the tab count too);
   * undefined while read. */
  schedules: readonly Schedule[] | undefined;
  /** Why the schedules could not be read, when they could not. */
  error: unknown;
}

/**
 * TemplateSchedulesTab is the template page's Schedules tab: the schedules
 * that run this template (the schedules list's own `template_id` filter),
 * each with its state — active, paused, or paused for review after an
 * assistant's change —, its timetable, its next run and what it does while
 * a run is still active. A schedule's name opens the Scheduler's editor
 * beside the list (read-only without `templates.write`); its menu pauses or
 * resumes it, edits or deletes it, or opens it on the Schedules page. "New
 * schedule" opens the same editor with this template chosen, and the new
 * schedule stays open here.
 */
export function TemplateSchedulesTab({ org, template, role, schedules, error }: TemplateSchedulesTabProps) {
  const t = useT();
  const s = t.templates.page.schedules;
  const navigate = useNavigate();
  const now = useNow();
  const canWrite = may(role, 'templates.write');
  const canRun = may(role, 'jobs.run');
  // The editor's template picker offers every template; until they are
  // read, this one (the one a new schedule starts with) is enough.
  const templates = useTemplates(org).data?.templates ?? [template];
  const [open, setOpen] = useState<string | null>(null);
  const actions = useScheduleActions(org, {
    canWrite,
    edit: setOpen,
    deleted: (id) => setOpen((current) => (current === id ? null : current)),
  });
  const openInScheduler = (id: string) => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id, template: template.id } });

  return (
    <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_420px]">
      <Section
        id="template-schedules"
        title={s.title}
        actions={
          canWrite && (
            <Button variant="primary" onClick={() => setOpen('new')}>
              <Clock size={14} /> {s.newSchedule}
            </Button>
          )
        }
      >
        <p className="text-[13px] text-muted-foreground">{s.hint}</p>
        {error ? (
          <p role="alert" className="text-xs text-destructive">
            {error instanceof Error ? error.message : s.readFailed}
          </p>
        ) : !schedules ? (
          <p className="text-[13px] text-muted-foreground">{t.common.loading}</p>
        ) : schedules.length === 0 ? (
          <p className="text-[13px] text-muted-foreground">{s.empty}</p>
        ) : (
          <Card>
            <ul aria-label={s.label}>
              {schedules.map((sc) => (
                <li
                  key={sc.id}
                  aria-label={sc.name}
                  className="grid items-center gap-3 border-b border-border px-4 py-2.5 text-[13px] last:border-b-0"
                  style={{ gridTemplateColumns: COLUMNS }}
                >
                  <button
                    type="button"
                    aria-label={s.open(sc.name)}
                    aria-pressed={open === sc.id}
                    onClick={() => setOpen(sc.id)}
                    className="min-w-0 truncate rounded text-left font-medium hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
                  >
                    {sc.name}
                  </button>
                  <span>
                    <ScheduleStatusPill schedule={sc} />
                  </span>
                  <span className="min-w-0 truncate" title={sc.description || undefined}>
                    <span className="font-mono text-xs">{sc.cron}</span>
                    {sc.description && <span className="text-xs text-muted-foreground"> · {sc.description}</span>}
                  </span>
                  <span className="text-xs text-muted-foreground">{sc.paused ? s.notWhilePaused : s.next(relative(sc.next_run_at, now, t))}</span>
                  <span className="min-w-0 truncate text-xs text-muted-foreground">{s.overlap[sc.guards.overlap === 'queue' ? 'queue' : 'skip']}</span>
                  <RowMenu label={s.menu(sc.name)}>
                    {actions(sc).map((a) =>
                      a.kind === 'link' ? null : (
                        <RowMenuItem key={a.id} onSelect={a.onSelect} disabled={a.disabled} destructive={a.destructive}>
                          {a.label}
                        </RowMenuItem>
                      ),
                    )}
                    <RowMenuItem onSelect={() => openInScheduler(sc.id)}>{s.inScheduler}</RowMenuItem>
                  </RowMenu>
                </li>
              ))}
            </ul>
          </Card>
        )}
      </Section>
      {open && (
        <ScheduleEditor
          key={open}
          org={org}
          id={open}
          templates={templates}
          defaultTemplate={template.id}
          canWrite={canWrite}
          canRun={canRun}
          onClose={() => setOpen(null)}
          onCreated={setOpen}
        />
      )}
    </div>
  );
}
