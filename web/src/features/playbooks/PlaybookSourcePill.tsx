import { Pill } from '../../ui/badge.tsx';

/** PlaybookSourcePill marks where a playbook comes from, the same on the
 * list and on the playbook page: accent for a repository, neutral for the
 * editor. The label is the caller's (a short word in a list cell, the
 * repository and branch in the page's header). */
export function PlaybookSourcePill({ source, label }: { source: string; label: string }) {
  return <Pill tone={source === 'repo' ? 'accent' : 'unknown'}>{label}</Pill>;
}
