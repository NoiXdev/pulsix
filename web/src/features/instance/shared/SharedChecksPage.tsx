import { useParams } from '@tanstack/react-router';
import { SharedListFrame, SharedRow } from './SharedListFrame.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useQueryGuard } from '../../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../../ui/QueryState.tsx';
import { useCheckDefinitions, useDeleteCheckDefinition } from '../../../api/hooks/useSharedCheckDefinitions.ts';
import { useT } from '../../../i18n/useT.ts';
import type { CheckDefinitionDef } from '../../../api/types.ts';

const COLUMNS = 'minmax(0, 0.8fr) minmax(0, 0.6fr) minmax(0, 1fr)';

/**
 * SharedChecksPage lists the installation's check definitions
 * (SharedReposPage's own shape, same gate, same delete flow). A definition's
 * shared half (spec decision 2) is the key, plugin, params and interval; the
 * binding and the enabled flag belong to the projection, which each
 * organization edits through its own Checks page.
 *
 * Unlike the other two kinds, a check definition's delete never refuses with
 * 409: nothing in the schema references check_definitions (internal/shared's
 * DeleteCheckDefinitionWithProjections), so the confirmation says what the
 * delete removes rather than warning about a dependant that cannot exist.
 */
export function SharedChecksPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const t = useT();
  const toast = useToast();
  const definitions = useCheckDefinitions();
  const remove = useDeleteCheckDefinition();

  const guard = useQueryGuard(definitions);
  if (guard) return guard;
  if (!definitions.data) return <QueryLoading />;
  const list = definitions.data.definitions ?? [];

  async function onDelete(d: CheckDefinitionDef) {
    if (!confirm(t.instance.shared.checks.confirmDelete(d.key))) return;
    try {
      await remove.mutateAsync(d.id);
      toast.show(t.instance.shared.checks.deleted(d.key));
    } catch (err) {
      toast.show(err instanceof Error ? err.message : t.instance.shared.deleteFailed, 'destructive');
    }
  }

  return (
    <SharedListFrame
      title={t.settings.tabs.sharedChecks}
      count={t.instance.shared.checks.count(list.length)}
      addLabel={t.instance.shared.checks.add}
      addTo="/o/$org/settings/shared/checks/new"
      org={org}
      columns={COLUMNS}
      headings={[t.instance.shared.checks.key, t.instance.shared.checks.plugin, t.instance.shared.checks.interval]}
      empty={t.instance.shared.checks.empty}
      isEmpty={list.length === 0}
    >
      {list.map((d) => (
        <SharedRow
          key={d.id}
          to="/o/$org/settings/shared/checks/$id"
          params={{ org, id: d.id }}
          columns={COLUMNS}
          name={d.key}
          onDelete={() => void onDelete(d)}
        >
          <span className="truncate font-mono text-[13px]">{d.key}</span>
          <span className="truncate text-xs">{d.plugin}</span>
          <span className="truncate text-xs text-muted-foreground">{d.interval_seconds}s</span>
        </SharedRow>
      ))}
    </SharedListFrame>
  );
}
