import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { RefreshCw } from 'lucide-react';
import { Button } from '../../../ui/button.tsx';
import { Pill } from '../../../ui/badge.tsx';
import { useToast } from '../../../ui/toastContext.ts';
import { useSyncRepo } from '../../../api/hooks/useRepos.ts';
import type { TemplateVariablesSource } from '../../../api/types.ts';
import { useFormat } from '../../../i18n/format.ts';
import { useT } from '../../../i18n/useT.ts';
import { sourceNotices } from './sourceNotices.ts';

export type VarsMode = 'off' | 'manual' | 'automatic';

const MODES: readonly VarsMode[] = ['off', 'manual', 'automatic'];

export interface SourcePanelProps {
  org: string;
  source?: TemplateVariablesSource;
  mode: VarsMode;
  /** templates.write on a template that is not a projection. */
  canWrite: boolean;
  /** repos.write: the panel offers to sync the repository anew. */
  canSyncRepo: boolean;
  /** Why the mode and "Sync now" are unavailable right now (unsaved
   * changes, a write in flight); absent when they are. */
  lockedReason?: string;
  onMode: (mode: VarsMode) => void;
  onSyncNow: () => void;
}

/**
 * SourcePanel shows where a template's variables come from (spec 7.75 goal
 * 5): the playbook (a repository path or the inline playbook), the commit
 * and time of the read, whether a newer read waits, the sync mode, "Sync
 * now" (the preview) and, for repos.write, a fresh repository sync. It
 * says why a read cannot change the template. The page decides what a mode
 * choice does (linking goes through the preview).
 */
export function SourcePanel({ org, source, mode, canWrite, canSyncRepo, lockedReason, onMode, onSyncNow }: SourcePanelProps) {
  const t = useT();
  const s = t.templates.source;
  const fmt = useFormat();
  const kind = source?.kind ?? '';
  const locked = !canWrite || lockedReason !== undefined;

  return (
    <section aria-label={s.title} className="flex flex-col gap-3 rounded-md border border-border bg-card px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <h2 className="text-sm font-semibold">{s.title}</h2>
        {kind === '' && <span className="text-xs text-muted-foreground">{s.noPlaybook}</span>}
        {kind === 'inline' && <span className="text-xs">{s.inline}</span>}
        {kind === 'repo' && source?.path && <span className="font-mono text-xs">{source.path}</span>}
        {kind === 'repo' && source?.commit && <span className="font-mono text-xs text-muted-foreground">{s.commit(source.commit.slice(0, 7))}</span>}
        {source?.synced_at && <span className="text-xs text-muted-foreground">{s.syncedAt(fmt.dateTime(source.synced_at))}</span>}
        {source?.newer && <Pill tone="accent">{kind === 'inline' ? s.newerInline : s.newer}</Pill>}
      </div>
      {source && sourceNotices(source, s).length > 0 && (
        <ul className="flex flex-col gap-0.5 text-xs text-muted-foreground">
          {sourceNotices(source, s).map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap items-end justify-between gap-3">
        <fieldset disabled={locked} className="flex flex-col gap-1">
          <legend className="mb-1 text-[11px] font-semibold text-muted-foreground">{s.mode}</legend>
          <div role="radiogroup" aria-label={s.mode} className="flex gap-3 text-xs">
            {MODES.map((m) => (
              <label key={m} className="flex items-center gap-1.5">
                <input type="radio" name="vars-mode" value={m} checked={mode === m} onChange={() => onMode(m)} />
                {s.modes[m]}
              </label>
            ))}
          </div>
          <span className="text-[11px] text-muted-foreground">{s.modeHint[mode]}</span>
        </fieldset>
        <div className="flex flex-wrap items-center gap-2">
          {canSyncRepo && kind === 'repo' && source?.repo_id && <RepoSync org={org} repoId={source.repo_id} />}
          {canWrite && mode !== 'off' && (
            <Button type="button" variant="primary" disabled={locked} onClick={onSyncNow}>
              {s.syncNow}
            </Button>
          )}
        </div>
      </div>
      {canWrite && lockedReason && <p className="text-[11px] text-muted-foreground">{lockedReason}</p>}
    </section>
  );
}

/** RepoSync fetches the repository anew (its own sync job, repos.write)
 * and links to that job's run instead of leaving the page. */
function RepoSync({ org, repoId }: { org: string; repoId: string }) {
  const t = useT();
  const s = t.templates.source;
  const toast = useToast();
  const sync = useSyncRepo(org);
  const [jobId, setJobId] = useState<string | null>(null);
  return (
    <>
      {jobId && (
        <Link to="/o/$org/runs/$id" params={{ org, id: jobId }} className="text-xs text-primary hover:underline">
          {s.syncRepoRun}
        </Link>
      )}
      <Button
        type="button"
        disabled={sync.isPending}
        onClick={async () => {
          try {
            const { job } = await sync.mutateAsync(repoId);
            setJobId(job.id);
            toast.show(s.syncRepoStarted);
          } catch (err) {
            toast.show(err instanceof Error ? err.message : s.syncRepoFailed, 'destructive');
          }
        }}
      >
        <RefreshCw size={13} /> {s.syncRepo}
      </Button>
    </>
  );
}
