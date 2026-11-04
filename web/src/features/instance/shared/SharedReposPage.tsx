import { useParams } from '@tanstack/react-router';
import { SharedListFrame, SharedRow } from './SharedListFrame.tsx';
import { useToast } from '../../../ui/toastContext.ts';
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
 * admits no other caller, so anyone else who reaches the route (the nav item
 * is hidden from them, the route itself is not guarded client-side) sees the
 * 403 page instead of the list. It lives under an organization's Settings
 * for navigation only — the definitions are instance-wide.
 *
 * A row opens SharedRepoFormPage; Delete… confirms and then deletes. A
 * definition a schedule, trigger or playbook still references refuses with
 * 409 (spec decision 5), and the server composes that sentence — naming the
 * organization and what references it — specifically so this screen can show
 * it verbatim, which is why the toast carries err.message.
 */
export function SharedReposPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const definitions = useRepoDefinitions();
  const remove = useDeleteRepoDefinition();

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  async function onDelete(d: RepoDefinition) {
    if (!confirm(t.instance.shared.repos.confirmDelete(d.name))) return;
    try {
      await remove.mutateAsync(d.id);
      toast.show(t.instance.shared.repos.deleted(d.name));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
    }
  }

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
