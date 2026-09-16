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

const COLUMNS = 'minmax(0, 0.6fr) minmax(0, 1fr) 130px 90px 90px 130px';

/** PlaybooksPage lists the playbooks jobs run against (spec §7, backlog
 * 2.9): repo-sourced ones (a path inside a registered repository, arriving
 * with a sync) and editor ones (an inline YAML document). Repo playbooks
 * have no editor — they are edited in their repository. */
export function PlaybooksPage() {
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
          <h1 className="text-xl font-semibold">Playbooks</h1>
          <span className="text-[13px] text-muted-foreground">
            {list.length} playbooks · a job template runs one of these against its scope
          </span>
        </div>
        {canWrite && (
          <Button variant="primary" onClick={() => setDialog('new')}>
            <FilePlus2 size={15} /> New editor playbook
          </Button>
        )}
      </div>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Source</span>
          <span>Summary / path</span>
          <span>Repository</span>
          <span>Updated</span>
        </GridHeader>
        {list.map((p) => (
          <GridRow key={p.id} columns={COLUMNS}>
            <span>
              {p.source === 'repo' ? <Pill tone="accent">repo</Pill> : <Pill tone="unknown">editor</Pill>}
            </span>
            <span className="truncate font-mono text-[13px]">
              {canWrite && p.source === 'editor' ? (
                <button type="button" className="truncate text-left hover:text-primary" onClick={() => setDialog(p)}>
                  {label(p)}
                </button>
              ) : (
                label(p)
              )}
            </span>
            <span className="truncate text-xs text-muted-foreground">{p.repo_id ? (repoName(p.repo_id) ?? p.repo_id.slice(0, 8)) : '—'}</span>
            <span className="text-xs text-muted-foreground">{p.updated_at.slice(0, 10)}</span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No playbooks yet — add a repository and sync it, or write one in the editor.</p>}
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

function label(p: Playbook): string {
  return p.source === 'repo' ? (p.path ?? '') : p.summary || '(unnamed play)';
}
