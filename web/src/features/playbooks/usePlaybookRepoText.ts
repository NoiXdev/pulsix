import { usePlaybookContent } from '../../api/hooks/usePlaybooks.ts';
import type { PlaybookTextLeftOut } from '../../api/types.ts';
import { errorStatus } from '../../lib/apiError.ts';

/** What the page can show of a repository playbook's text. `earlier` is
 * true when the text is kept from an earlier sync whose successor left the
 * playbook out (the server's `from_earlier_sync`; `commit` is "" then). */
export type RepoText =
  | { state: 'loading' }
  | { state: 'failed'; message: string }
  | { state: 'not-synced' }
  | { state: 'no-route' }
  | { state: 'text'; content: string; commit: string; earlier: boolean; syncedAt: string }
  | { state: 'left-out'; reason: PlaybookTextLeftOut };

/** The manager's answer for a repository that was never synced
 * (internal/api/playbook_content_handlers.go). */
const NOT_SYNCED = 'the repository has not been synced yet';

/** The states in which there is no text to show. */
export type RepoNoText = Exclude<RepoText['state'], 'text'>;

/**
 * usePlaybookRepoText reads a repository playbook's text as of the last
 * sync (backlog 3.127) and says which of the page's states it is in: the
 * text, the reason the sync stored none, a repository that was never
 * synced (the 404 with the server's own sentence), a server without the
 * route (any other 404), or a read that failed.
 */
export function usePlaybookRepoText(org: string, id: string, enabled: boolean): RepoText {
  const query = usePlaybookContent(org, id, enabled);
  if (query.isError) {
    if (errorStatus(query.error) === 404) return { state: query.error.message === NOT_SYNCED ? 'not-synced' : 'no-route' };
    return { state: 'failed', message: query.error.message };
  }
  const data = query.data;
  if (!data) return { state: 'loading' };
  if (data.content !== undefined) return { state: 'text', content: data.content, commit: data.commit, earlier: data.from_earlier_sync, syncedAt: data.synced_at };
  return { state: 'left-out', reason: data.left_out ?? 'not_sent' };
}
