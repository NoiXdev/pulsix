import type { Messages } from '../../i18n/types.ts';

/** chainTemplateName is the template name a chain field carries, or the
 * localised "deleted template" label when the API sent "" because the
 * template no longer exists. */
export function chainTemplateName(name: string | undefined, t: Messages): string {
  return name || t.runs.detail.chain.deletedTemplate;
}
