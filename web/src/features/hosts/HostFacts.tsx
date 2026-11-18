import type { ReactNode } from 'react';
import { Copy } from 'lucide-react';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useHostAgent } from '../../api/hooks/useAgents.ts';
import type { Host } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { useCopy } from '../../ui/useCopy.ts';
import { systemLabel } from './hostOverview.ts';
import { useHostRoute } from './useHostRoute.ts';

const NONE = '—';

/**
 * HostFacts is the second line of the host header: the address (with a
 * copy button) and the zone always, then what is known — the operating
 * system from the facts, the SSH route when it goes through a jump host,
 * and the agent when there is one. What is not known is left out rather
 * than shown as a dash.
 */
export function HostFacts({ org, host }: { org: string; host: Host }) {
  const t = useT();
  const f = t.hosts.detail.fact;
  const copy = useCopy();
  const zones = useZones(org);
  const agent = useHostAgent(org, host.id);
  const route = useHostRoute(org, host);
  const zone = zones.data?.zones?.find((z) => z.id === host.zone_id);
  const system = systemLabel(host.facts);
  const a = agent.data?.agent;

  const items: { label: string; value: ReactNode }[] = [
    {
      label: f.address,
      value: host.address ? (
        <span className="inline-flex items-center gap-1">
          <span className="font-mono">{host.address}</span>
          <button
            type="button"
            aria-label={t.hosts.detail.copyAddress}
            title={t.hosts.detail.copyAddress}
            onClick={() => void copy(host.address, t.hosts.detail.addressCopied)}
            className="rounded p-0.5 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary"
          >
            <Copy size={12} />
          </button>
        </span>
      ) : (
        NONE
      ),
    },
    // Nothing until the zones are known: a piece of the id would read as
    // a zone name. Only a zone that is truly missing falls back to it.
    { label: f.zone, value: <span className="font-mono">{zone?.slug ?? (zones.data ? host.zone_id.slice(0, 8) : NONE)}</span> },
  ];
  if (system) items.push({ label: f.system, value: system });
  if (route && route.names.length > 0) items.push({ label: f.route, value: <span className="font-mono">{f.via(route.names.join(' → '))}</span> });
  if (a) {
    const status = a.status in t.agents.status ? t.agents.status[a.status] : a.status;
    items.push({
      label: f.agent,
      value: <span className={a.status === 'online' ? 'text-[var(--px-ok)]' : 'text-[var(--px-warn)]'}>{a.version ? `${status} · ${a.version}` : status}</span>,
    });
  }

  return (
    <dl data-testid="host-facts" className="flex flex-wrap items-center gap-x-5 gap-y-1 text-[13px]">
      {items.map((item) => (
        <div key={item.label} className="flex min-w-0 items-center gap-1.5">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className="min-w-0 break-words">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}
