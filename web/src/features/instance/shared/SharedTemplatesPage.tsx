import { useParams } from '@tanstack/react-router';
import { SharedListFrame, SharedRow } from './SharedListFrame.tsx';
import { useDeleteWithConfirm } from './useDeleteWithConfirm.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { ListRowName } from '../../../ui/list-row.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useRepoDefinitions } from '../../../api/hooks/useSharedRepoDefinitions.ts';
import { useDeleteTemplateDefinition, useTemplateDefinitions } from '../../../api/hooks/useSharedTemplateDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { TemplateDefinition } from '../../../api/types.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 0.8fr) minmax(0, 1.1fr) 110px';

/**
 * SharedTemplatesPage lists the installation's job template definitions
 * (SharedReposPage's own shape, same gate, same delete flow). A definition's
 * shared half (spec decision 2) is the name, the shared repository its
 * playbook resolves against, the playbook's path, the limit and the extra
 * vars; the scope and the SSH credential stay each organization's own.
 *
 * A delete a schedule or job trigger still references refuses with 409
 * naming the organization (job_template_vault_credentials cascade on
 * purpose and never appear in that message).
 */
export function SharedTemplatesPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const definitions = useTemplateDefinitions();
  const repoDefinitions = useRepoDefinitions();
  const remove = useDeleteTemplateDefinition();
  const onDelete = useDeleteWithConfirm<TemplateDefinition>({
    remove: remove.mutateAsync,
    confirmMessage: (d) => t.instance.shared.templates.confirmDelete(d.name),
    deletedMessage: (d) => t.instance.shared.templates.deleted(d.name),
  });

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];
  // The repository column shows the repository's name, from the repository
  // list's own query. Until that loads (or if it fails) the id's head shows
  // instead — a legitimate state, not an error.
  const repoName = new Map((repoDefinitions.data?.definitions ?? []).map((d) => [d.id, d.name]));

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedTemplates}
      count={t.instance.shared.templates.count(list.length)}
      addLabel={t.instance.shared.templates.add}
      addTo="/o/$org/settings/shared/templates/new"
      org={org}
      columns={COLUMNS}
      headings={[
        t.instance.shared.name,
        t.instance.shared.templates.repository,
        t.instance.shared.templates.playbookPath,
        t.instance.shared.templates.limit,
      ]}
      empty={t.instance.shared.templates.empty}
      isEmpty={list.length === 0}
    >
      {list.map((d) => (
        <SharedRow
          key={d.id}
          to="/o/$org/settings/shared/templates/$id"
          params={{ org, id: d.id }}
          columns={COLUMNS}
          name={d.name}
          onDelete={() => void onDelete(d)}
        >
          <ListRowName className="truncate font-mono text-[13px]">{d.name}</ListRowName>
          <span className="truncate font-mono text-xs text-muted-foreground">
            {repoName.get(d.shared_repo_id) ?? d.shared_repo_id.slice(0, 8)}
          </span>
          <span className="truncate font-mono text-xs">{d.playbook_path}</span>
          <span className="truncate font-mono text-xs text-muted-foreground">{d.limit || '—'}</span>
        </SharedRow>
      ))}
    </SharedListFrame>
  );
}
