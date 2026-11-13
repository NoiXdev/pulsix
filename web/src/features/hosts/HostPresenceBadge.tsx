import { Pill, Tag } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';
import { seenLabel } from '../workers/expiry.ts';
import type { HostPresence } from './presence.ts';

/**
 * HostPresenceBadge marks a client host (backlog 3.57): "Off" for a client
 * whose agent is offline — in the neutral tone, never a state colour, since
 * the manager is not judging it — and a small "Client" tag for one that is
 * on. A server gets nothing, so it looks exactly as before. The title of
 * the Off pill says when the agent was last seen.
 */
export function HostPresenceBadge({ presence }: { presence: HostPresence }) {
  const t = useT();
  if (presence.kind === 'server') return null;
  if (presence.kind === 'client') return <Tag className="shrink-0 font-sans">{t.hosts.presence.client}</Tag>;
  return (
    <span className="shrink-0" title={t.hosts.presence.lastSeen(seenLabel(presence.lastSeen, new Date(), t))}>
      <Pill tone="unknown">{t.hosts.presence.off}</Pill>
    </span>
  );
}
