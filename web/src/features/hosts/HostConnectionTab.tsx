import { Section } from '../../ui/detail-layout.tsx';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { ConnectionTestCard } from './ConnectionTestCard.tsx';
import { HostAgentCard } from './HostAgentCard.tsx';
import { HostSshCard } from './HostSshCard.tsx';
import { ManagementOffHint } from './hostHints.tsx';
import type { HostActions } from './useHostActions.tsx';

/**
 * HostConnectionTab is the host page's Connection tab: the agent, and for
 * a managed host how Pulsix reaches it over SSH and the connection test.
 * An unmanaged host keeps its agent; the management-off hint, with the way
 * to switch management on, stands in for the rest.
 */
export function HostConnectionTab({ org, host, role, actions }: { org: string; host: Host; role: string; actions: HostActions }) {
  const t = useT();
  const canWrite = may(role, 'inventory.write');
  return (
    <Section id="connection" title={t.hosts.detail.section.connection}>
      <HostAgentCard org={org} hostId={host.id} hostName={host.name} zoneId={host.zone_id} canWrite={may(role, 'agents.write')} />
      {host.managed ? (
        <>
          <HostSshCard org={org} hostId={host.id} revision={host.revision} managed canWrite={canWrite} canCreateCredential={may(role, 'credentials.write')} />
          <ConnectionTestCard org={org} hostId={host.id} canWrite={canWrite} />
        </>
      ) : (
        <ManagementOffHint actions={actions} withAction />
      )}
    </Section>
  );
}
