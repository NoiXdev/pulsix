import { Link } from '@tanstack/react-router';
import { ListPlus } from 'lucide-react';
import { buttonClass } from '../../ui/buttonStyles.ts';
import { DataList, CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import { useSchedules } from '../../api/hooks/useSchedules.ts';
import { useTriggers } from '../../api/hooks/useTriggers.ts';
import type { Template } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';
import { may } from '../../lib/roles.ts';
import { useListView } from '../lists/useListView.ts';
import { useTemplateListActions } from '../templates/useTemplateListActions.tsx';

/**
 * PlaybookUsedByTab is the playbook page's "Used by" tab: the job templates
 * that run the playbook, each with its limit, its schedules and its webhook
 * triggers (read per listed template, through the endpoints the templates'
 * own dialogs use). A row opens the template's dialog (for a reader: its
 * variables page) and has the templates list's own menu (Run, Variables,
 * Triggers, Schedules, Edit, Delete).
 * "Create template…" opens the new template dialog on the templates page
 * with this playbook chosen (`?create=`). When the templates cannot be
 * read, an alert line takes the list's place.
 */
export function PlaybookUsedByTab({
  org,
  role,
  playbookId,
  templates,
  error,
}: {
  org: string;
  role: string;
  playbookId: string;
  templates: readonly Template[] | undefined;
  /** Why the templates could not be read; null while they can. */
  error: unknown;
}) {
  const t = useT();
  const u = t.playbooks.page.used;
  const canWrite = may(role, 'templates.write');
  const actions = useTemplateListActions(org, {
    canWrite,
    canRun: may(role, 'jobs.run'),
    canReadCredentials: may(role, 'credentials.read'),
    canCreateCredential: may(role, 'credentials.write'),
    canEnrolAgents: may(role, 'agents.write'),
  });
  const columns = usedByColumns(t, org);
  const view = useListView('playbook-templates', columns, { mode: 'client', urlState: false, rowKey: (tpl) => tpl.id });
  const { rows, total } = view.view(view.ready ? templates : undefined);

  return (
    <>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-sm font-semibold">{u.title}</h2>
        {canWrite && (
          <Link to="/o/$org/templates" params={{ org }} search={{ create: playbookId }} className={buttonClass('default')}>
            <ListPlus size={14} /> {u.create}
          </Link>
        )}
      </div>
      {error ? (
        <p role="alert" className="text-xs text-destructive">
          {error instanceof Error ? error.message : t.playbooks.page.templatesFailed}
        </p>
      ) : (
        <DataList
          label={u.label}
          columns={columns}
          rows={rows}
          rowKey={(tpl) => tpl.id}
          rowLabel={(tpl) => tpl.name}
          rowActions={actions.rowActions}
          // A writer opens the template's dialog, as on the templates list; a
          // reader, who may not edit it, the Variables tab of its page (read-only
          // there).
          rowLink={canWrite ? undefined : (tpl) => ({ to: '/o/$org/templates/$id', params: { org, id: tpl.id }, search: { tab: 'variables' }, label: tpl.name })}
          onRowActivate={actions.edit}
          loading={!templates || !view.ready}
          empty={u.none}
          {...view.listProps(total, t.templates.list.count)}
        />
      )}
      <p className="text-[11px] text-muted-foreground">{u.deleteNote}</p>
      {actions.dialogs}
    </>
  );
}

function usedByColumns(t: Messages, org: string): DataListColumn<Template>[] {
  const c = t.playbooks.page.used.columns;
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 1fr)',
      sortKey: 'name',
      sortValue: (tpl) => tpl.name,
      required: true,
      defaultVisible: true,
      render: (tpl) => <span className="block truncate">{tpl.name}</span>,
    },
    {
      id: 'limit',
      label: c.limit,
      width: 'minmax(0, 0.6fr)',
      sortKey: 'limit',
      sortValue: (tpl) => tpl.limit || null,
      defaultVisible: true,
      render: (tpl) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{tpl.limit || '—'}</span>,
    },
    {
      id: 'schedules',
      label: c.schedules,
      width: 'minmax(0, 1fr)',
      defaultVisible: true,
      render: (tpl) => <TemplateSchedules org={org} templateId={tpl.id} />,
    },
    {
      id: 'triggers',
      label: c.triggers,
      width: 'minmax(0, 1fr)',
      defaultVisible: true,
      render: (tpl) => <TemplateTriggers org={org} template={tpl} />,
    },
  ];
}

/** Names, comma-separated, or the column's "none"; nothing while loading,
 * and a short "could not be read" when the read failed. */
function Names({ names, none, failed }: { names: string[] | undefined; none: string; failed: boolean }) {
  const t = useT();
  if (failed) return <span className={`block truncate text-destructive ${CELL_MINOR}`}>{t.playbooks.page.used.readFailed}</span>;
  if (!names) return null;
  return <span className={`block truncate ${names.length ? '' : 'text-muted-foreground'} ${CELL_MINOR}`}>{names.length ? names.join(', ') : none}</span>;
}

function TemplateSchedules({ org, templateId }: { org: string; templateId: string }) {
  const t = useT();
  const schedules = useSchedules(org, templateId);
  return <Names names={schedules.data?.schedules?.map((s) => s.name)} none={t.playbooks.page.used.noSchedules} failed={schedules.isError} />;
}

function TemplateTriggers({ org, template }: { org: string; template: Template }) {
  const t = useT();
  const triggers = useTriggers(org, { kind: 'template_run', id: template.id, name: template.name });
  return <Names names={triggers.data?.triggers?.map((x) => x.name)} none={t.playbooks.page.used.noTriggers} failed={triggers.isError} />;
}
