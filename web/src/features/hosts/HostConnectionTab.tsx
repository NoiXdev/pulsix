import { KeyRound } from 'lucide-react';
import { Section } from '../../ui/detail-layout.tsx';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { ConnectionTestCard } from './ConnectionTestCard.tsx';
import { HostAgentCard } from './HostAgentCard.tsx';
import { HostSshSummary } from './HostSshSummary.tsx';
import { ManagementOffHint } from './hostHints.tsx';
import type { HostActions } from './useHostActions.tsx';

/**
 * HostConnectionTab is the host page's Connection tab: for a managed host
 * how Pulsix reaches it over SSH (a summary; "Edit …" opens the SSH access
 * dialog) and the connection test, then the agent, then a pointer to the
 * key snippets in the dialog. An unmanaged host keeps its agent; the
 * management-off hint, with the way to switch management on, stands in for
 * the SSH side.
 */
export function HostConnectionTab({
  org, host, role, actions, onSshAccess, onKeySnippets,
}: {
  org: string;
  host: Host;
  role: string;
  actions: HostActions;
  onSshAccess: () => void;
  onKeySnippets: () => void;
}) {
  const t = useT();
  const canWrite = may(role, 'inventory.write');
  return (
    <Section id="connection" title={t.hosts.detail.section.connection}>
      {host.managed ? (
        <>
          <HostSshSummary org={org} host={host} canEdit={canWrite} canReadCredentials={may(role, 'credentials.read')} onEdit={onSshAccess} />
          <ConnectionTestCard org={org} hostId={host.id} canWrite={canWrite} />
        </>
      ) : (
        <ManagementOffHint actions={actions} withAction />
      )}
      <HostAgentCard org={org} hostId={host.id} hostName={host.name} zoneId={host.zone_id} canWrite={may(role, 'agents.write')} />
      {host.managed && canWrite && <KeySnippetsCard onOpen={onKeySnippets} />}
    </Section>
  );
}

function KeySnippetsCard({ onOpen }: { onOpen: () => void }) {
  const t = useT();
  const c = t.hosts.connectionTab;
  return (
    <Card className="flex flex-wrap items-center gap-3 p-4">
      <KeyRound size={16} className="shrink-0 text-muted-foreground" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <h3 className="text-[13px] font-semibold">{c.snippets}</h3>
        <p className="text-xs text-muted-foreground">{c.snippetsBody}</p>
      </div>
      <Button type="button" onClick={onOpen}>
        {c.showSnippets}
      </Button>
    </Card>
  );
}
