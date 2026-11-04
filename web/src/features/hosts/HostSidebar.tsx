import { useState, type ReactNode } from 'react';
import { Pencil, Play } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { Switch } from '../../ui/switch.tsx';
import { SidebarFacts } from '../../ui/detail-layout.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useHostAgent } from '../../api/hooks/useAgents.ts';
import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import type { Host } from '../../api/types.ts';
import { errorStatus } from '../../lib/apiError.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { BootstrapDialog } from './BootstrapDialog.tsx';
import { HostTagsEditor } from './HostTagsEditor.tsx';
import { systemLabel } from './hostOverview.ts';
import { useHostAlertSummary } from './useHostAlertSummary.ts';
import type { HostActions } from './useHostActions.tsx';

const NONE = '—';

/**
 * HostSidebar is the host page's left column: who the host is (name, a
 * state pill from its open alerts, the facts an operator looks up most),
 * its two capabilities as switches, its tags, and the actions. The section
 * nav below it is DetailLayout's.
 */
export function HostSidebar({ org, host, actions }: { org: string; host: Host; actions: HostActions }) {
  const t = useT();
  const role = useOrg(org).data?.org.role;
  const [bootstrapOpen, setBootstrapOpen] = useState(false);
  const canBootstrap = host.managed && may(role, 'inventory.write');

  return (
    <div className="flex flex-col gap-4">
      <HostHeading org={org} host={host} canRead={may(role, 'checks.read')} />
      <div data-testid="host-facts">
        <HostFacts org={org} host={host} />
      </div>

      <Group label={t.hosts.detail.capabilities}>
        <CapabilitySwitch label={t.hosts.capability.managed} checked={host.managed} disabled={!actions.canSwitch} onChange={actions.toggleManagement} />
        <CapabilitySwitch label={t.hosts.capability.monitored} checked={host.monitored} disabled={!actions.canSwitch} onChange={actions.toggleMonitoring} />
      </Group>

      <Group label={t.hosts.detail.tags}>
        <HostTagsEditor org={org} hostId={host.id} canWrite={may(role, 'hosts.write')} />
      </Group>

      {(actions.canRun || actions.canEdit || canBootstrap || actions.canDelete) && (
        <div className="flex flex-wrap items-center gap-2">
          {actions.canRun && (
            <Button variant="primary" onClick={actions.runPlay}>
              <Play size={14} /> {t.hosts.detail.runPlay}
            </Button>
          )}
          {actions.canEdit && (
            <Button onClick={actions.edit}>
              <Pencil size={14} /> {t.hosts.detail.edit}
            </Button>
          )}
          {(canBootstrap || actions.canDelete) && (
            <RowMenu label={t.hosts.detail.more(host.name)}>
              {canBootstrap && <RowMenuItem onSelect={() => setBootstrapOpen(true)}>{t.hosts.detail.bootstrap}</RowMenuItem>}
              {actions.canDelete && (
                <RowMenuItem onSelect={actions.remove} destructive>
                  {t.hosts.detail.delete}
                </RowMenuItem>
              )}
            </RowMenu>
          )}
        </div>
      )}
      {bootstrapOpen && <BootstrapDialog org={org} hostId={host.id} hostName={host.name} onClose={() => setBootstrapOpen(false)} />}
    </div>
  );
}

const DOT: Record<Tone, string> = {
  ok: 'var(--px-ok)',
  warn: 'var(--px-warn)',
  crit: 'var(--px-crit)',
  unknown: 'var(--px-unknown)',
  accent: 'var(--px-accent)',
  maintenance: 'var(--px-maintenance)',
};

/** The name with a status dot and the state pill. The state is the open
 * alerts' (crit over warn over OK); a host nobody monitors has none, and
 * its pill says so rather than "OK". */
function HostHeading({ org, host, canRead }: { org: string; host: Host; canRead: boolean }) {
  const t = useT();
  const summary = useHostAlertSummary(org, host.id, canRead);
  let tone: Tone = 'unknown';
  let label: string | null = null;
  if (!host.monitored) {
    label = t.hosts.detail.health.monitoringOff;
  } else if (summary) {
    tone = summary.tone;
    label =
      summary.tone === 'crit'
        ? t.hosts.detail.health.critical(summary.crit)
        : summary.tone === 'warn'
          ? t.hosts.detail.health.warnings(summary.open)
          : t.hosts.detail.health.ok;
  }

  return (
    <div className="flex flex-col items-start gap-1.5">
      <div className="flex min-w-0 items-center gap-2.5">
        <span
          className="h-2.5 w-2.5 shrink-0 rounded-full"
          style={{ background: DOT[tone] }}
          title={label ?? t.hosts.detail.health.unknown}
        />
        <h1 className="min-w-0 font-mono text-xl font-bold break-all">{host.name}</h1>
      </div>
      {label && <Pill tone={tone}>{label}</Pill>}
    </div>
  );
}

/** Address, zone, system, agent and the resolved jump host. */
function HostFacts({ org, host }: { org: string; host: Host }) {
  const t = useT();
  const zones = useZones(org);
  const agent = useHostAgent(org, host.id);
  // Only a managed host is reached over SSH, so only it has a route.
  const preview = useHostInventoryPreview(org, host.managed ? host.id : '');
  const bastions = useBastions(org);

  const zone = zones.data?.zones?.find((z) => z.id === host.zone_id);
  const a = agent.data?.agent;
  let agentValue: ReactNode = NONE;
  if (a) {
    const status = a.status in t.agents.status ? t.agents.status[a.status] : a.status;
    agentValue = <span className={a.status === 'online' ? 'text-[var(--px-ok)]' : 'text-[var(--px-warn)]'}>{a.version ? `${status} · ${a.version}` : status}</span>;
  } else if (agent.isError && errorStatus(agent.error) === 404) {
    agentValue = t.hosts.detail.fact.noAgent;
  }

  let jump: string = NONE;
  const connection = preview.data?.connection;
  if (host.managed && connection) {
    const chain = connection.bastions ?? [];
    const name = (id: string) => bastions.data?.bastions?.find((b) => b.id === id)?.name ?? id.slice(0, 8);
    jump = chain.length > 0 ? chain.map(name).join(' → ') : t.hosts.detail.fact.direct;
  }

  return (
    <SidebarFacts
      items={[
        { label: t.hosts.detail.fact.address, value: <span className="font-mono">{host.address || NONE}</span> },
        { label: t.hosts.detail.fact.zone, value: <span className="font-mono">{zone?.slug ?? host.zone_id.slice(0, 8)}</span> },
        { label: t.hosts.detail.fact.system, value: systemLabel(host.facts) ?? NONE },
        { label: t.hosts.detail.fact.agent, value: agentValue },
        { label: t.hosts.detail.fact.jumpHost, value: <span className="font-mono">{jump}</span> },
      ]}
    />
  );
}

function Group({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2">
      <span className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">{label}</span>
      {children}
    </div>
  );
}

function CapabilitySwitch({ label, checked, disabled, onChange }: { label: string; checked: boolean; disabled: boolean; onChange: () => void }) {
  return (
    <div className="flex items-center justify-between gap-3 text-[13px]">
      <span>{label}</span>
      <Switch checked={checked} disabled={disabled} aria-label={label} onCheckedChange={onChange} />
    </div>
  );
}
