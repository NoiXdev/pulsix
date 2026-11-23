import { usePlaybookContent } from '../../api/hooks/usePlaybooks.ts';
import type { PlaybookTextLeftOut } from '../../api/types.ts';
import { errorStatus } from '../../lib/apiError.ts';

/** What the page can show of a repository playbook's text. `earlier` is
 * true when the text is kept from an earlier sync whose successor left the
 * playbook out: the server's `from_earlier_sync`, or — from a server that
 * does not send the flag yet — an empty `commit`. */
export type RepoText =
  | { state: 'loading' }
  | { state: 'failed'; message: string }
  | { state: 'not-synced' }
  | { state: 'no-route' }
  | { state: 'text'; content: string; commit: string; earlier: boolean; syncedAt: string }
  | { state: 'left-out'; reason: PlaybookTextLeftOut };

/** The manager's answer for a repository that was never synced
 * (internal/api/playbook_content_handlers.go, pinned by its test). */
const NOT_SYNCED = 'the repository has not been synced yet';

/** The manager's answer for a playbook that does not exist (any more):
 * the route is there, so it is no server without the route. */
const PLAYBOOK_GONE = 'not found';

/** The states in which there is no text to show. */
export type RepoNoText = Exclude<RepoText['state'], 'text'>;

/**
 * usePlaybookRepoText reads a repository playbook's text as of the last
 * sync (backlog 3.127) and says which of the page's states it is in: the
 * text, the reason the sync stored none, a repository that was never
 * synced (the 404 with the server's own sentence), a server without the
 * route (any other 404 but the playbook's own), or a read that failed.
 */
export function usePlaybookRepoText(org: string, id: string, enabled: boolean): RepoText {
  const query = usePlaybookContent(org, id, enabled);
  if (query.isError) {
    if (errorStatus(query.error) === 404 && query.error.message === NOT_SYNCED) return { state: 'not-synced' };
    if (errorStatus(query.error) === 404 && query.error.message !== PLAYBOOK_GONE) return { state: 'no-route' };
    return { state: 'failed', message: query.error.message };
  }
  const data = query.data;
  if (!data) return { state: 'loading' };
  if (data.content !== undefined) {
    // Absent from a server older than the flag: an empty commit said it there.
    const flag: boolean | undefined = data.from_earlier_sync;
    return { state: 'text', content: data.content, commit: data.commit, earlier: flag ?? data.commit === '', syncedAt: data.synced_at };
  }
  return { state: 'left-out', reason: data.left_out ?? 'not_sent' };
}
