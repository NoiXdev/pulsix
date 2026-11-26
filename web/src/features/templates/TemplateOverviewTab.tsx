import type { Schedule, Template, Trigger } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { SectionAction } from '../hosts/HostOverviewCards.tsx';
import { TemplateRequestsSection } from './TemplateRequestsSection.tsx';
import { TemplateRunsSection } from './TemplateRunsSection.tsx';
import { TemplateStartsSection } from './TemplateStartsSection.tsx';
import type { TemplateTab } from './templateTabs.ts';

const COLUMNS = 'grid grid-cols-1 items-start gap-8 xl:grid-cols-2';

interface TemplateOverviewTabProps {
  org: string;
  template: Template;
  /** The template's schedules and triggers (the page reads them for the
   * tab counts and the review notice too); undefined while read. */
  schedules: readonly Schedule[] | undefined;
  triggers: readonly Trigger[] | undefined;
  onTab: (tab: TemplateTab) => void;
}

/**
 * TemplateOverviewTab is what the template page opens on: its recent runs
 * and its open run requests beside what starts it (schedules, webhook
 * triggers) and what it starts (its follow-ups), each with the way to its
 * own tab.
 */
export function TemplateOverviewTab({ org, template, schedules, triggers, onTab }: TemplateOverviewTabProps) {
  const t = useT();
  const o = t.templates.page.overview;
  return (
    <div className={COLUMNS}>
      <div className="flex min-w-0 flex-col gap-8">
        <TemplateRunsSection org={org} templateId={template.id} actions={<SectionAction onClick={() => onTab('runs')}>{o.allRuns}</SectionAction>} />
        <TemplateRequestsSection org={org} templateId={template.id} />
      </div>
      <div className="flex min-w-0 flex-col gap-8">
        <TemplateStartsSection org={org} template={template} schedules={schedules} triggers={triggers} onTab={onTab} />
      </div>
    </div>
  );
}
