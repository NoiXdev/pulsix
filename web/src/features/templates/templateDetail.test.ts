import { describe, expect, it } from 'vitest';
import type { Playbook, Repo } from '../../api/types.ts';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';
import { openRequests, pausedForReview, targetHosts, templateSource } from './templateDetail.ts';

const playbook = (over: Partial<Playbook> = {}): Playbook => ({
  id: 'pb1', source: 'repo', repo_id: 'r1', path: 'site.yml', summary: 'site.yml', revision: 1, created_at: '', updated_at: '', ...over,
});

const repo = (over: Partial<Repo> = {}): Repo => ({
  id: 'r1', name: 'infra', url: 'git@example:infra.git', branch: 'main', subpath: '', created_at: '', updated_at: '', vault_identities: null,
  last_synced_commit: 'abc123def4567890', last_synced_at: '2026-11-26T08:00:00Z', ...over,
});

describe('templateSource', () => {
  it('says a template whose playbook is not synced yet has none', () => {
    expect(templateSource('', undefined, undefined)).toEqual({ kind: 'unsynced' });
  });

  it('waits while the playbook is not known', () => {
    expect(templateSource('pb1', undefined, [repo()])).toEqual({ kind: 'pending' });
  });

  it('names the editor for an editor playbook', () => {
    expect(templateSource('pb1', playbook({ source: 'editor', repo_id: undefined }), [])).toEqual({ kind: 'editor' });
  });

  it('names the repository, its branch and its last synced commit', () => {
    expect(templateSource('pb1', playbook(), [repo()])).toEqual({
      kind: 'repo',
      repoId: 'r1',
      name: 'infra',
      branch: 'main',
      commit: 'abc123def4567890',
      syncedAt: '2026-11-26T08:00:00Z',
    });
  });

  it('keeps a repository it cannot name to a piece of its id, without branch or commit', () => {
    expect(templateSource('pb1', playbook({ repo_id: 'r9abcdef1234' }), [repo()])).toEqual({ kind: 'repo', repoId: 'r9abcdef1234', name: 'r9abcdef' });
    expect(templateSource('pb1', playbook(), undefined)).toEqual({ kind: 'repo', repoId: 'r1', name: 'r1' });
  });

  it('leaves out a commit the repository never synced', () => {
    expect(templateSource('pb1', playbook(), [repo({ last_synced_commit: undefined, last_synced_at: undefined })])).toEqual({
      kind: 'repo', repoId: 'r1', name: 'infra', branch: 'main',
    });
  });
});

describe('targetHosts', () => {
  const hosts = (...names: string[]) => names.map((name, i) => ({ id: `h${i}`, name }));

  it('passes on pending and an unresolved scope', () => {
    expect(targetHosts({ kind: 'pending' })).toEqual({ kind: 'pending' });
    expect(targetHosts({ kind: 'open' })).toEqual({ kind: 'scope' });
  });

  it('names up to three hosts and counts the rest', () => {
    expect(targetHosts({ kind: 'listed', hosts: hosts('a', 'b') })).toEqual({ kind: 'listed', names: ['a', 'b'], more: 0, total: 2 });
    expect(targetHosts({ kind: 'listed', hosts: hosts('a', 'b', 'c', 'd', 'e') })).toEqual({ kind: 'listed', names: ['a', 'b', 'c'], more: 2, total: 5 });
  });

  it('says when the scope resolves to no host', () => {
    expect(targetHosts({ kind: 'listed', hosts: [] })).toEqual({ kind: 'listed', names: [], more: 0, total: 0 });
  });
});

describe('pausedForReview', () => {
  // Paused or disabled by a person: nothing waits for a review.
  const pausedByHand = { paused: true };
  const disabledByHand = { enabled: false };

  it('is null when nothing waits for a review', () => {
    expect(pausedForReview([pausedByHand, {}], [disabledByHand])).toBeNull();
    expect(pausedForReview([], [])).toBeNull();
  });

  it('counts the paused schedules and the disabled triggers and collects their reviews once each', () => {
    expect(
      pausedForReview(
        [{ paused_reason: 'assistant_change', review_request_id: 'r1' }, pausedByHand],
        [{ disabled_reason: 'assistant_change', review_request_id: 'r1' }, { disabled_reason: 'assistant_change', review_request_id: 'r2' }],
      ),
    ).toEqual({ count: 3, reviewIds: ['r1', 'r2'] });
  });

  it('has no review id for an item that has none', () => {
    expect(pausedForReview([{ paused_reason: 'assistant_change' }], [])).toEqual({ count: 1, reviewIds: [] });
  });
});

describe('openRequests', () => {
  const rq = (id: string, over: Partial<RunRequest> = {}): RunRequest =>
    ({ id, kind: 'run', template_id: 't1', reason: id, status: 'pending', requested_by_user_id: 'u', requested_by_token_name: 'x', template_revision: 1, created_at: '2026-11-26T08:00:00Z', ...over }) as RunRequest;

  it('keeps the pending and waiting requests of this template, the soonest to expire first', () => {
    const list = [
      rq('other', { template_id: 't2' }),
      rq('late', { expires_at: '2026-11-26T12:00:00Z' }),
      rq('done', { status: 'approved' }),
      rq('waiting', { status: 'waiting_for_sync' }),
      rq('soon', { expires_at: '2026-11-26T09:00:00Z' }),
      rq('gone', { status: 'expired' }),
    ];
    expect(openRequests(list, 't1').map((r) => r.id)).toEqual(['soon', 'late', 'waiting']);
  });

  it('is empty for no requests', () => {
    expect(openRequests(undefined, 't1')).toEqual([]);
  });
});
