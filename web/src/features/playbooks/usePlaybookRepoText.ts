import { usePlaybookContent } from '../../api/hooks/usePlaybooks.ts';
import type { PlaybookTextLeftOut } from '../../api/types.ts';
import { errorStatus } from '../../lib/apiError.ts';

/** What the page can show of a repository playbook's text. `commit` is ""
 * when the text is kept from an earlier sync whose successor left the
 * playbook out. */
export type RepoText =
  | { state: 'loading' }
  | { state: 'failed'; message: string }
  | { state: 'not-synced' }
  | { state: 'text'; content: string; commit: string; syncedAt: string }
  | { state: 'left-out'; reason: PlaybookTextLeftOut };

/**
 * usePlaybookRepoText reads a repository playbook's text as of the last
 * sync (backlog 3.127) and says which of the page's states it is in: the
 * text, the reason the sync stored none, a repository that was never
 * synced (404), or a read that failed.
 */
export function usePlaybookRepoText(org: string, id: string, enabled: boolean): RepoText {
  const query = usePlaybookContent(org, id, enabled);
  if (query.isError) {
    if (errorStatus(query.error) === 404) return { state: 'not-synced' };
    return { state: 'failed', message: query.error.message };
  }
  const data = query.data;
  if (!data) return { state: 'loading' };
  if (data.content !== undefined) return { state: 'text', content: data.content, commit: data.commit, syncedAt: data.synced_at };
  return { state: 'left-out', reason: data.left_out ?? 'not_sent' };
}
