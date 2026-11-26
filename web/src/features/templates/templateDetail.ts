import type { Playbook, Repo } from '../../api/types.ts';
import type { RunRequest } from '../../api/hooks/useRunRequests.ts';
import type { TemplateHosts } from '../playbooks/useTemplateHosts.ts';
import { sortRequests } from '../runs/runRequestStatus.ts';

/** Where a template's playbook comes from, as the template page's header
 * says it: not synced yet (a projection whose playbook this organization
 * has not synced), not known yet, the playbook editor, or a repository with
 * its branch and the commit its last sync read. */
export type TemplateSource =
  | { kind: 'unsynced' }
  | { kind: 'pending' }
  | { kind: 'editor' }
  | { kind: 'repo'; repoId: string; name: string; branch?: string; commit?: string; syncedAt?: string };

/** templateSource resolves a template's playbook (`playbookId`, "" when not
 * synced) against the organization's playbooks and repositories. A
 * repository the list does not hold (or not yet) is named by a piece of its
 * id, without branch or commit. */
export function templateSource(playbookId: string, playbook: Playbook | undefined, repos: readonly Repo[] | undefined): TemplateSource {
  if (playbookId === '') return { kind: 'unsynced' };
  if (!playbook) return { kind: 'pending' };
  if (playbook.source !== 'repo' || !playbook.repo_id) return { kind: 'editor' };
  const repo = repos?.find((r) => r.id === playbook.repo_id);
  if (!repo) return { kind: 'repo', repoId: playbook.repo_id, name: playbook.repo_id.slice(0, 8) };
  const out: TemplateSource = { kind: 'repo', repoId: repo.id, name: repo.name, branch: repo.branch };
  if (repo.last_synced_commit) out.commit = repo.last_synced_commit;
  if (repo.last_synced_commit && repo.last_synced_at) out.syncedAt = repo.last_synced_at;
  return out;
}

/** What the header says about a template's target hosts: still being
 * read, only its scope (the whole organization, or a scope the browser
 * cannot resolve: the server does), or the first few hosts by name and how
 * many more there are. */
export type TargetHosts = { kind: 'pending' } | { kind: 'scope' } | { kind: 'listed'; names: string[]; more: number; total: number };

/** targetHosts shortens a template's resolved hosts (useTemplateHosts) to
 * the first `max` names and a count of the rest. */
export function targetHosts(hosts: TemplateHosts, max = 3): TargetHosts {
  if (hosts.kind === 'pending') return { kind: 'pending' };
  if (hosts.kind === 'open') return { kind: 'scope' };
  const names = hosts.hosts.slice(0, max).map((h) => h.name);
  return { kind: 'listed', names, more: hosts.hosts.length - names.length, total: hosts.hosts.length };
}

/** What waits for a review: how many of the template's schedules and
 * triggers an assistant's change paused, and the review requests that
 * resume them (each once). */
export interface ReviewPause {
  count: number;
  reviewIds: string[];
}

/** The fields of a schedule and a trigger that say an assistant's change
 * paused it (spec 2026-11-25 C2). Optional on purpose: a manager that does
 * not send them pauses nothing for review. */
interface PausableSchedule {
  paused?: boolean;
  paused_reason?: string;
  review_request_id?: string;
}
interface PausableTrigger {
  enabled?: boolean;
  disabled_reason?: string;
  review_request_id?: string;
}

/** pausedForReview is null when none of the schedules and triggers waits
 * for a review. */
export function pausedForReview(schedules: readonly PausableSchedule[], triggers: readonly PausableTrigger[]): ReviewPause | null {
  const paused = [...schedules.filter((s) => s.paused_reason === 'assistant_change'), ...triggers.filter((tr) => tr.disabled_reason === 'assistant_change')];
  if (paused.length === 0) return null;
  const reviewIds = [...new Set(paused.map((p) => p.review_request_id).filter((id): id is string => !!id))];
  return { count: paused.length, reviewIds };
}

/** openRequests is a template's run requests that still wait — for a
 * decision, or approved and waiting for their sync — in the requests
 * list's order (the soonest to expire first). */
export function openRequests(requests: readonly RunRequest[] | null | undefined, templateId: string): RunRequest[] {
  const open = (requests ?? []).filter((r) => r.template_id === templateId && (r.status === 'pending' || r.status === 'waiting_for_sync'));
  return sortRequests(open);
}
