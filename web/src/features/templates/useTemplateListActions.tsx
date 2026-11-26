import { useState, type ReactNode } from 'react';
import { useNavigate } from '@tanstack/react-router';
import type { Template } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import type { RowAction } from '../../ui/data-list/index.ts';
import { TriggersDialog } from '../triggers/TriggersDialog.tsx';
import { TemplateDialog } from './TemplateDialog.tsx';
import { useDeleteTemplateCommand } from './useDeleteTemplateCommand.ts';
import { useRunTemplate } from './useRunTemplate.tsx';

type OpenDialog = { kind: 'new' } | { kind: 'edit' | 'triggers'; template: Template };

export interface TemplateListActionsOptions {
  canWrite: boolean;
  canRun: boolean;
  canReadCredentials: boolean;
  canCreateCredential: boolean;
  /** agents.write: switching a template's agent enrollment needs it. */
  canEnrolAgents: boolean;
}

/**
 * useTemplateListActions is the templates list's row menu for every row:
 * Open (the template's page), Run (a template with follow-ups asks first, in the "run with follow-ups"
 * dialog, so they can be changed for this run), Variables, Triggers,
 * Schedules, Edit and Delete (not for a projected template: its dialog
 * offers none either). `dialogs` holds the one dialog an entry opened;
 * `openNew` is the page's "Add template".
 */
export function useTemplateListActions(org: string, opts: TemplateListActionsOptions): { rowActions: (tpl: Template) => RowAction[]; dialogs: ReactNode; openNew: () => void; edit?: (row: Template) => void } {
  const t = useT();
  const navigate = useNavigate();
  const runner = useRunTemplate(org);
  const remove = useDeleteTemplateCommand(org);
  const [dialog, setDialog] = useState<OpenDialog | null>(null);
  const close = () => setDialog(null);

  function rowActions(tpl: Template): RowAction[] {
    const items: RowAction[] = [
      { id: 'open', label: t.templates.list.open, onSelect: () => void navigate({ to: '/o/$org/templates/$id', params: { org, id: tpl.id }, search: { tab: undefined } }) },
    ];
    if (opts.canRun) items.push({ id: 'run', label: t.templates.list.run, onSelect: () => void runner.run(tpl), disabled: runner.isPending });
    items.push(
      { id: 'variables', label: t.templates.list.variables, onSelect: () => void navigate({ to: '/o/$org/templates/$id/variables', params: { org, id: tpl.id } }) },
      { id: 'triggers', label: t.templates.list.triggers, onSelect: () => setDialog({ kind: 'triggers', template: tpl }) },
      {
        id: 'schedules',
        label: t.templates.list.schedules,
        onSelect: () => void navigate({ to: '/o/$org/schedules', params: { org }, search: { id: undefined, template: tpl.id } }),
      },
    );
    if (opts.canWrite) items.push({ id: 'edit', label: t.common.edit, onSelect: () => setDialog({ kind: 'edit', template: tpl }) });
    if (opts.canWrite && !tpl.shared_template_id) {
      items.push({ id: 'delete', label: t.common.delete, destructive: true, onSelect: () => void remove.run(tpl), disabled: remove.isPending });
    }
    return items;
  }

  return {
    rowActions,
    edit: opts.canWrite ? (template) => setDialog({ kind: 'edit', template }) : undefined,
    openNew: () => setDialog({ kind: 'new' }),
    dialogs: (
      <>
        {runner.dialog}
        {dialog?.kind === 'triggers' && (
          <TriggersDialog
            org={org}
            target={{ kind: 'template_run', id: dialog.template.id, name: dialog.template.name }}
            canWrite={opts.canWrite}
            canRun={opts.canRun}
            onClose={close}
          />
        )}
        {dialog?.kind === 'new' && (
          <TemplateDialog
            org={org}
            canReadCredentials={opts.canReadCredentials}
            canCreateCredential={opts.canCreateCredential}
            canEnrolAgents={opts.canEnrolAgents}
            onClose={close}
          />
        )}
        {dialog?.kind === 'edit' && (
          <TemplateDialog
            org={org}
            template={{ id: dialog.template.id, name: dialog.template.name }}
            canReadCredentials={opts.canReadCredentials}
            canCreateCredential={opts.canCreateCredential}
            canEnrolAgents={opts.canEnrolAgents}
            onClose={close}
          />
        )}
      </>
    ),
  };
}
