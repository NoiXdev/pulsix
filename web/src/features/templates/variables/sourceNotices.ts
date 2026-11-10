import type { TemplateVariablesSource } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

type SourceMessages = Messages['templates']['source'];

/** unavailableReason says, in the page's language, why a read cannot
 * change the template (the server's `reason`, which is English text), or
 * null when it can: no read yet, a worker that reads no sources, a
 * playbook the last sync left out (an editor playbook too large to read),
 * or one that was not read. */
export function unavailableReason(src: TemplateVariablesSource, s: SourceMessages): string | null {
  if (src.available) return null;
  // A record (it has a sync time) from a worker that reads no sources: that
  // is why the playbook is missing from it too, and a sync by the same
  // worker would not help.
  if (src.kind === 'repo' && src.synced_at && !src.worker_reads) return s.workerReads;
  if (!src.found) return s.notFound;
  // An editor playbook has no sync: it is left out when its variables are
  // over the cap, and saving a shorter text is what helps.
  if (src.left_out) return src.kind === 'inline' ? s.inlineLeftOut : s.leftOut;
  return s.notRead;
}

/** sourceNotices are what a person should know about a read, in the order
 * they fix them: what an inline playbook does not read, the repository's
 * record, then why this playbook's read cannot be used. */
export function sourceNotices(src: TemplateVariablesSource, s: SourceMessages): string[] {
  const out: string[] = [];
  if (src.kind === 'inline') {
    out.push(s.inlineRoles);
    // The left-out reason below already says the playbook is too large.
    if (src.incomplete && !src.left_out) out.push(s.inlineIncomplete);
  } else if (src.incomplete) {
    out.push(s.incomplete);
  }
  const reason = unavailableReason(src, s);
  if (reason) out.push(reason);
  return out;
}
