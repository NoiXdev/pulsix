import { Pill } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

export type CapabilityKind = 'managed' | 'monitored';

/**
 * CapabilityCell shows one of a host's two capabilities in the list as a
 * read-only pill: "Managed" / "Monitored" in the ok style when on, a muted
 * "not managed" / "not monitored" when off. It is deliberately not
 * operable: the whole row is a link, and switching a capability is a row
 * menu action (or the switch on the host page).
 */
export function CapabilityCell({ kind, on }: { kind: CapabilityKind; on: boolean }) {
  const t = useT();
  if (on) return <Pill tone="ok">{t.hosts.capability[kind]}</Pill>;
  return (
    <span className="truncate text-xs text-muted-foreground">
      {kind === 'managed' ? t.hosts.capability.notManaged : t.hosts.capability.notMonitored}
    </span>
  );
}
