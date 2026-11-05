import type { TemplateVariablesSource } from '../../../api/types.ts';
import type { Messages } from '../../../i18n/types.ts';

type SourceMessages = Messages['templates']['source'];

/** unavailableReason says, in the page's language, why a read cannot
 * change the template (the server's `reason`, which is English text), or
 * null when it can: no read yet, a worker that reads no sources, a
 * playbook the last sync left out, or one that was not read. */
export function unavailableReason(src: TemplateVariablesSource, s: SourceMessages): string | null {
  if (src.available) return null;
  if (!src.found) return s.notFound;
  if (src.kind === 'repo' && !src.worker_reads) return s.workerReads;
  if (src.left_out) return s.leftOut;
  return s.notRead;
}

/** sourceNotices are what a person should know about a read, in the order
 * they fix them: what an inline playbook does not read, the repository's
 * record, then why this playbook's read cannot be used. */
export function sourceNotices(src: TemplateVariablesSource, s: SourceMessages): string[] {
  const out: string[] = [];
  if (src.kind === 'inline') out.push(s.inlineRoles);
  if (src.kind === 'repo' && !src.worker_reads && src.available) out.push(s.workerReads);
  if (src.incomplete) out.push(s.incomplete);
  const reason = unavailableReason(src, s);
  if (reason) out.push(reason);
  return out;
}
