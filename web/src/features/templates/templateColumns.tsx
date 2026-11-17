import { Pill } from '../../ui/badge.tsx';
import { CELL_MINOR, type DataListColumn } from '../../ui/data-list/index.ts';
import type { Template } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { ReviewBadges } from './variables/ReviewBadges.tsx';

export interface TemplateColumnsContext {
  t: Messages;
  playbookLabel: (id: string) => string;
  groupName: (id: string) => string;
  /** The default credential's name, undefined when this page does not know it. */
  credentialName: (id: string) => string | undefined;
}

/** What a template's scope says, as text: the whole organization, a group,
 * an explicit host selection, or both halves at once. */
function scopeText(tpl: Template, groupName: (id: string) => string, t: Messages): string {
  const count = (tpl.scope.host_ids ?? []).length;
  if (!tpl.scope.group_id && count === 0) return t.templates.scope.whole;
  const group = tpl.scope.group_id ? groupName(tpl.scope.group_id) : null;
  if (!group) return t.templates.scope.hosts(count);
  if (count === 0) return group;
  return t.templates.scope.groupAndHosts(group, count);
}

/**
 * templateColumns is every column the job templates list offers (list id
 * `templates`), sorted in the browser. The name column carries the
 * installation-managed pill and the variables review badges (a projection
 * of an installation-owned definition: TemplateDialog disables its fields,
 * so the row says why before the dialog opens). Run, variables, triggers,
 * schedules, edit and delete are the row menu's (useTemplateListActions).
 */
export function templateColumns({ t, playbookLabel, groupName, credentialName }: TemplateColumnsContext): DataListColumn<Template>[] {
  const c = t.templates.list.columns;
  const credentialOf = (tpl: Template) => (tpl.credential_id ? (credentialName(tpl.credential_id) ?? tpl.credential_id.slice(0, 8)) : '');
  return [
    {
      id: 'name',
      label: c.name,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'name',
      sortValue: (tpl) => tpl.name,
      required: true,
      defaultVisible: true,
      render: (tpl) => (
        <span className="flex min-w-0 flex-wrap items-center gap-1.5">
          <span className="truncate font-mono">{tpl.name}</span>
          {tpl.shared_template_id && <Pill tone="accent">{t.templates.list.managed}</Pill>}
          {/* What a linked template's variables leave for a person
              (backlog 7.75); the variables page resolves it. */}
          <ReviewBadges review={tpl.variables_review} />
        </span>
      ),
    },
    {
      id: 'playbook',
      label: c.playbook,
      width: 'minmax(0, 1fr)',
      sortKey: 'playbook',
      sortValue: (tpl) => playbookLabel(tpl.playbook_id),
      defaultVisible: true,
      render: (tpl) => <span className={`block truncate font-mono text-muted-foreground ${CELL_MINOR}`}>{playbookLabel(tpl.playbook_id)}</span>,
    },
    {
      id: 'scope',
      label: c.scope,
      width: 'minmax(0, 0.9fr)',
      sortKey: 'scope',
      sortValue: (tpl) => scopeText(tpl, groupName, t),
      defaultVisible: true,
      render: (tpl) => (
        <span>
          <Pill tone={!tpl.scope.group_id && (tpl.scope.host_ids ?? []).length === 0 ? 'unknown' : 'accent'}>{scopeText(tpl, groupName, t)}</Pill>
        </span>
      ),
    },
    {
      id: 'credential',
      label: c.credential,
      width: 'minmax(0, 0.8fr)',
      sortKey: 'credential',
      sortValue: (tpl) => credentialOf(tpl) || null,
      defaultVisible: true,
      render: (tpl) => <span className={`block truncate text-muted-foreground ${CELL_MINOR}`}>{credentialOf(tpl) || '—'}</span>,
    },
  ];
}
