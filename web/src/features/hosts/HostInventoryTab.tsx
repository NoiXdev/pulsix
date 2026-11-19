import { Card } from '../../ui/table.tsx';
import { Section } from '../../ui/detail-layout.tsx';
import { may } from '../../lib/roles.ts';
import type { Host } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { HostInventoryCard } from './HostInventoryCard.tsx';
import { ManagementOffHint } from './hostHints.tsx';
import type { HostActions } from './useHostActions.tsx';

/**
 * HostInventoryTab is the host page's "Inventory & facts" tab: the host's
 * inventory (groups and variables, which only a managed host has; the
 * management-off hint stands in otherwise), then every fact it reported.
 */
export function HostInventoryTab({ org, host, role, actions }: { org: string; host: Host; role: string; actions: HostActions }) {
  const t = useT();
  const s = t.hosts.detail.section;
  return (
    <>
      <Section id="inventory" title={s.inventory}>
        {host.managed ? <HostInventoryCard org={org} hostId={host.id} hostName={host.name} managed canWrite={may(role, 'inventory.write')} /> : <ManagementOffHint actions={actions} />}
      </Section>
      <Section id="facts" title={s.facts}>
        <FactsCard facts={host.facts} />
      </Section>
    </>
  );
}

/** Every fact the host reported, as reported. */
function FactsCard({ facts }: { facts: Host['facts'] }) {
  const t = useT();
  const entries = Object.entries(facts ?? {});
  return (
    <Card className="px-4 py-2">
      {entries.length === 0 ? (
        <span className="py-1.5 text-xs text-muted-foreground">{t.hosts.detail.noFacts}</span>
      ) : (
        <dl className="flex flex-col">
          {entries.map(([k, v]) => (
            <div key={k} className="flex justify-between gap-4 border-b border-border py-1.5 text-xs last:border-b-0">
              <dt className="text-muted-foreground">{k}</dt>
              <dd className="min-w-0 font-mono break-all text-foreground">{String(v)}</dd>
            </div>
          ))}
        </dl>
      )}
    </Card>
  );
}
