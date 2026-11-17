import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { FilePlus2 } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { DataList, type RowAction } from '../../ui/data-list/index.ts';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { useListView } from '../lists/useListView.ts';
import { PlaybookEditor } from './PlaybookEditor.tsx';
import { playbookColumns, playbookLabel } from './playbookColumns.tsx';
import type { Playbook } from '../../api/types.ts';

/** PlaybooksPage lists the playbooks jobs run against (spec §7, backlog
 * 2.9): repo-sourced ones (a path inside a registered repository, arriving
 * with a sync) and editor ones (an inline YAML document). Repo playbooks
 * have no editor — they are edited in their repository. */
export function PlaybooksPage() {
  const t = useT();
  const { org } = useParams({ strict: false }) as { org: string };
  const orgInfo = useOrg(org);
  const playbooks = usePlaybooks(org);
  const repos = useRepos(org);
  const canWrite = may(orgInfo.data?.org.role, 'playbooks.write');

  const [dialog, setDialog] = useState<'new' | Playbook | null>(null);

  const columns = playbookColumns({ t, repoName: (id) => (repos.data?.repos ?? []).find((r) => r.id === id)?.name });
  const view = useListView('playbooks', columns, { mode: 'client', rowKey: (p) => p.id });

  const guard = useQueryGuard(playbooks);
  if (guard) return guard;
  if (!playbooks.data) return <QueryLoading />;
  const list = playbooks.data.playbooks ?? [];
  const { rows, total } = view.view(view.ready ? list : undefined);

  // Repo playbooks have no editor: they are edited in their repository.
  const rowActions = (p: Playbook): RowAction[] =>
    canWrite && p.source === 'editor' ? [{ id: 'edit', label: t.common.edit, onSelect: () => setDialog(p) }] : [];

  return (
    <>
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-2">
          <h1 className="text-xl font-semibold">{t.playbooks.list.title}</h1>
          <span className="text-[13px] text-muted-foreground">
            {t.playbooks.list.summary(list.length)}
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog('new')}>
            <FilePlus2 size={15} /> {t.playbooks.list.add}
          </Button>
        )}
      </div>
      <DataList
        label={t.playbooks.list.label}
        columns={columns}
        rows={rows}
        rowKey={(p) => p.id}
        rowLabel={(p) => playbookLabel(p, t)}
        rowActions={rowActions}
        loading={!view.ready}
        empty={t.playbooks.list.empty}
        {...view.listProps(total, t.playbooks.list.count)}
      />
      {dialog === 'new' ? (
        <PlaybookEditor org={org} onClose={() => setDialog(null)} />
      ) : dialog && dialog.source === 'editor' ? (
        <PlaybookEditor
          org={org}
          playbook={{ id: dialog.id, summary: dialog.summary }}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </>
  );
}
