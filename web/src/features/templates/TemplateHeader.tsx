import { useNavigate } from '@tanstack/react-router';
import { FlaskConical, Pencil, Play } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import type { Template } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { ReviewBadges } from './variables/ReviewBadges.tsx';
import { TemplateFacts } from './TemplateFacts.tsx';
import { useDeleteTemplateCommand } from './useDeleteTemplateCommand.ts';
import { useRunTemplate } from './useRunTemplate.tsx';

interface TemplateHeaderProps {
  org: string;
  template: Template;
  role: string;
  /** Opens the Settings tab with its form editable; absent on the
   * Settings tab, which offers its own Edit. */
  onEdit?: () => void;
  /** Called once the template is deleted, before the page leaves: the
   * page releases its unsaved-changes guard so leaving asks nothing. */
  onDeleted?: () => Promise<void>;
}

/**
 * TemplateHeader is the top of the template page, full width above its
 * tabs: the name with its badges (installation-managed, enrols agents,
 * follow-ups, what its variables leave for a person), the facts —
 * playbook, where it comes from, the commit last synced, the target hosts
 * — and on the right the actions: Run (asking first when the run enrols
 * agents or has follow-ups, useRunTemplate), Dry run (the playbook's Dry
 * run tab), Edit (the Settings tab, editable; left to the tab itself
 * while it is open) and the "⋯" menu with Delete. Each keeps the gating it has on the templates list.
 */
export function TemplateHeader({ org, template, role, onEdit, onDeleted }: TemplateHeaderProps) {
  const t = useT();
  const p = t.templates.page;
  const followUps = (template.follow_ups ?? []).length;

  return (
    <div data-testid="template-header" className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
      <div className="flex min-w-0 flex-1 flex-col gap-2.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
          <h1 className="min-w-0 font-mono text-xl font-bold break-all">{template.name}</h1>
          <span data-testid="template-badges" className="flex flex-wrap items-center gap-1.5">
            {template.shared_template_id && <Pill tone="accent">{t.templates.list.managed}</Pill>}
            {template.agent_enrollment && <Tag className="font-sans">{p.badges.enrols}</Tag>}
            {followUps > 0 && <Tag className="font-sans">{p.badges.followUps(followUps)}</Tag>}
            <ReviewBadges review={template.variables_review} />
          </span>
        </div>
        <TemplateFacts org={org} template={template} />
      </div>
      <TemplateHeaderActions org={org} template={template} role={role} onEdit={onEdit} onDeleted={onDeleted} />
    </div>
  );
}

function TemplateHeaderActions({ org, template, role, onEdit, onDeleted }: TemplateHeaderProps) {
  const t = useT();
  const p = t.templates.page;
  const navigate = useNavigate();
  const runner = useRunTemplate(org);
  const remove = useDeleteTemplateCommand(org);
  const canRun = may(role, 'jobs.run');
  const canWrite = may(role, 'templates.write');
  // A projection's dialog never offered Delete: the installation owns it.
  const canDelete = canWrite && !template.shared_template_id;
  if (!canRun && !canWrite) return null;

  async function onDelete() {
    if (!(await remove.run(template))) return;
    await onDeleted?.();
    await navigate({ to: '/o/$org/templates', params: { org } });
  }

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2">
      {canRun && (
        <Button variant="primary" onClick={() => void runner.run(template)} disabled={runner.isPending}>
          <Play size={14} /> {p.actions.run}
        </Button>
      )}
      {canRun && (
        <Button
          // A template whose playbook this organization has not synced yet
          // has no playbook page to dry-run on.
          disabled={!template.playbook_id}
          title={template.playbook_id ? undefined : t.templates.playbook.notSynced}
          onClick={() => void navigate({ to: '/o/$org/playbooks/$id', params: { org, id: template.playbook_id }, search: { tab: 'dry-run' } })}
        >
          <FlaskConical size={14} /> {p.actions.dryRun}
        </Button>
      )}
      {canWrite && onEdit && (
        <Button onClick={onEdit}>
          <Pencil size={14} /> {p.actions.edit}
        </Button>
      )}
      {canDelete && (
        <RowMenu label={p.actions.more(template.name)} triggerClassName="h-[34px] w-[34px] rounded-md border border-border bg-card text-foreground hover:bg-accent">
          <RowMenuItem onSelect={() => void onDelete()} disabled={remove.isPending} destructive>
            {p.actions.delete}
          </RowMenuItem>
        </RowMenu>
      )}
      {runner.dialog}
    </div>
  );
}
