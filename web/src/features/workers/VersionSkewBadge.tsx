import { Pill, type Tone } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

const TONE = { update_required: 'warn', newer: 'accent' } as const satisfies Record<string, Tone>;

/** VersionSkewBadge marks a worker or agent whose build is outside what the
 * manager supports (backlog 7.3): `update_required` (older than the previous
 * minor version) and `newer` (ahead of the manager). Every other status —
 * current, previous_minor, unknown (a dev build, no version) — shows
 * nothing, as does a value this build does not know. Shared by the Workers
 * page, the Agents page and the host agent card. */
export function VersionSkewBadge({ status }: { status?: string }) {
  const t = useT();
  if (status !== 'update_required' && status !== 'newer') return null;
  return (
    <span className="shrink-0 whitespace-nowrap">
      <Pill tone={TONE[status]}>{t.workers.versionStatus[status]}</Pill>
    </span>
  );
}
