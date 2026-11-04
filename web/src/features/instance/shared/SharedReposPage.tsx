import { useParams } from '@tanstack/react-router';
import { SharedListFrame, SharedRow } from './SharedListFrame.tsx';
import { useDeleteWithConfirm } from './useDeleteWithConfirm.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useDeleteRepoDefinition, useRepoDefinitions } from '../../../api/hooks/useSharedRepoDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { RepoDefinition } from '../../../api/types.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 1.3fr) 110px minmax(0, 0.9fr)';

/**
 * SharedReposPage lists the installation's repository definitions: the
 * provider's own repositories, defined once for every organization (spec
 * v0.2f, decisions 1-8) and projected into each as an ordinary, read-only
 * row — each organization still supplies its own credential, which never
 * belongs to a definition (decision 3).
 *
 * Reachable with Principal.InstanceAdmin only: GET /admin/repos/definitions
 * admits no other caller. The nav item is hidden from everyone else and the
 * route itself is not guarded client-side, so anyone else who reaches it —
 * this list, or the form pages, which load the same list first
 * (DefinitionLoader) — sees the 403 page rather than a screen that could
 * only fail. It lives under an organization's Settings for navigation only —
 * the definitions are instance-wide.
 *
 * A row opens SharedRepoFormPage; Delete… confirms and then deletes
 * (useDeleteWithConfirm, which also shows a refused delete's own message).
 */
export function SharedReposPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const definitions = useRepoDefinitions();
  const remove = useDeleteRepoDefinition();
  const onDelete = useDeleteWithConfirm<RepoDefinition>({
    remove: remove.mutateAsync,
    confirmMessage: (d) => t.instance.shared.repos.confirmDelete(d.name),
    deletedMessage: (d) => t.instance.shared.repos.deleted(d.name),
  });

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedRepos}
      count={t.instance.shared.repos.count(list.length)}
      addLabel={t.instance.shared.repos.add}
      addTo="/o/$org/settings/shared/repos/new"
      org={org}
      columns={COLUMNS}
      headings={[t.instance.shared.name, t.instance.shared.repos.url, t.instance.shared.repos.branch, t.instance.shared.repos.subpath]}
      empty={t.instance.shared.repos.empty}
      isEmpty={list.length === 0}
    >
      {list.map((d) => (
        <SharedRow
          key={d.id}
          to="/o/$org/settings/shared/repos/$id"
          params={{ org, id: d.id }}
          columns={COLUMNS}
          name={d.name}
          onDelete={() => void onDelete(d)}
        >
          <span className="truncate font-mono text-[13px]">{d.name}</span>
          <span className="truncate font-mono text-xs text-muted-foreground">{d.url}</span>
          <span className="truncate font-mono text-xs">{d.branch}</span>
          <span className="truncate font-mono text-xs text-muted-foreground">{d.subpath || '—'}</span>
        </SharedRow>
      ))}
    </SharedListFrame>
  );
}
