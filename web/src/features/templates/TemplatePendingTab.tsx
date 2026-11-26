import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { Button } from '../../ui/button.tsx';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { DisabledHint } from '../../ui/detail-layout.tsx';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { TriggersDialog } from '../triggers/TriggersDialog.tsx';

/** The tabs whose content still lives on its own page. */
export type PendingTab = 'variables' | 'schedules' | 'triggers' | 'runs';

/**
 * TemplatePendingTab stands in for a tab of the template page whose content
 * still lives elsewhere — the variables page, the schedules and runs lists
 * narrowed to the template, the triggers dialog — and leads there. Each is
 * replaced by the tab's own content (backlog 1.53, the page's second step).
 */
export function TemplatePendingTab({ org, template, role, tab }: { org: string; template: Template; role: string; tab: PendingTab }) {
  const t = useT();
  const p = t.templates.page;
  const [triggersOpen, setTriggersOpen] = useState(false);
  const link = buttonClass('default');

  const action = {
    variables: (
      <Link to="/o/$org/templates/$id/variables" params={{ org, id: template.id }} className={link}>
        {p.elsewhere.variables}
      </Link>
    ),
    schedules: (
      <Link to="/o/$org/schedules" params={{ org }} search={{ template: template.id }} className={link}>
        {p.elsewhere.schedules}
      </Link>
    ),
    triggers: <Button onClick={() => setTriggersOpen(true)}>{p.elsewhere.triggers}</Button>,
    runs: (
      <Link to="/o/$org/runs" params={{ org }} search={{ template: template.id }} className={link}>
        {p.elsewhere.runs}
      </Link>
    ),
  }[tab];

  return (
    <>
      <DisabledHint title={p.tabs[tab]} body={p.elsewhere.hint} action={action} />
      {triggersOpen && (
        <TriggersDialog
          org={org}
          target={{ kind: 'template_run', id: template.id, name: template.name }}
          canWrite={may(role, 'templates.write')}
          canRun={may(role, 'jobs.run')}
          onClose={() => setTriggersOpen(false)}
        />
      )}
    </>
  );
}
