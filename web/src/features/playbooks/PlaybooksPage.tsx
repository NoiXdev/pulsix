import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { FilePlus2 } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill } from '../../ui/badge.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useRepos } from '../../api/hooks/useRepos.ts';
import { usePlaybooks } from '../../api/hooks/usePlaybooks.ts';
import { may } from '../../lib/roles.ts';
import { PlaybookEditor } from './PlaybookEditor.tsx';
import type { Playbook } from '../../api/types.ts';
import type { Messages } from '../../i18n/types.ts';
import { useT } from '../../i18n/useT.ts';

const COLUMNS = 'minmax(0, 0.6fr) minmax(0, 1fr) 130px 90px 90px 130px';

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

  const guard = useQueryGuard(playbooks);
  if (guard) return guard;
  if (!playbooks.data) return <QueryLoading />;
  const list = playbooks.data.playbooks ?? [];
  const repoName = (id?: string) => (repos.data?.repos ?? []).find((r) => r.id === id)?.name;

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
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>{t.playbooks.list.columns.source}</span>
          <span>{t.playbooks.list.columns.summaryPath}</span>
          <span>{t.playbooks.list.columns.repository}</span>
          <span>{t.playbooks.list.columns.updated}</span>
        </GridHeader>
        {list.map((p) => (
          <GridRow key={p.id} columns={COLUMNS}>
            <span>
              {p.source === 'repo' ? <Pill tone="accent">{t.playbooks.list.sourceRepo}</Pill> : <Pill tone="unknown">{t.playbooks.list.sourceEditor}</Pill>}
            </span>
            <span className="truncate font-mono text-[13px]">
              {canWrite && p.source === 'editor' ? (
                <button type="button" className="truncate text-left hover:text-primary" onClick={() => setDialog(p)}>
                  {label(p, t)}
                </button>
              ) : (
                label(p, t)
              )}
            </span>
            <span className="truncate text-xs text-muted-foreground">{p.repo_id ? (repoName(p.repo_id) ?? p.repo_id.slice(0, 8)) : '—'}</span>
            <span className="text-xs text-muted-foreground">{p.updated_at.slice(0, 10)}</span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">{t.playbooks.list.empty}</p>}
      </Card>
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

function label(p: Playbook, t: Messages): string {
  return p.source === 'repo' ? (p.path ?? '') : p.summary || t.playbooks.list.unnamed;
}
