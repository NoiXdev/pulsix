import { useState } from 'react';
import { FileCode, Terminal } from 'lucide-react';
import { Button } from '../../ui/button.tsx';
import { Pill, Tag, type Tone } from '../../ui/badge.tsx';
import { RowMenu, RowMenuItem } from '../../ui/list-row.tsx';
import { useHostAgent } from '../../api/hooks/useAgents.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { seenLabel } from '../workers/expiry.ts';
import { BootstrapDialog } from './BootstrapDialog.tsx';
import { HostFacts } from './HostFacts.tsx';
import { HostPresenceBadge } from './HostPresenceBadge.tsx';
import { HostTagsEditor } from './HostTagsEditor.tsx';
import { hostPresence } from './presence.ts';
import { useHostAlertSummary } from './useHostAlertSummary.ts';
import { useRunConnectionTest } from './useRunConnectionTest.ts';
import type { HostActions } from './useHostActions.tsx';

interface HostHeaderProps {
  org: string;
  host: Host;
  role: string;
  actions: HostActions;
  /** Opens the host's SSH settings (the "SSH access …" menu entry). */
  onSshAccess: () => void;
}

/**
 * HostHeader is the top of the host page, full width above its tabs: the
 * name with its state and the capabilities that are on, the facts an
 * operator looks up most, the tags, and on the right the actions — Run
 * command and Run template as buttons, everything else in the "⋯" menu.
 * Each action keeps the gating it has everywhere (useHostActions; SSH
 * access, the connection test and Bootstrap need a managed host and
 * `inventory.write`). The capabilities are switched in the Edit dialog.
 */
export function HostHeader({ org, host, role, actions, onSshAccess }: HostHeaderProps) {
  return (
    <div className="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
      <div className="flex min-w-0 flex-1 flex-col gap-2.5">
        <HostTitle org={org} host={host} canRead={may(role, 'checks.read')} />
        <HostFacts org={org} host={host} />
        <HostTagsEditor org={org} hostId={host.id} revision={host.revision} canWrite={may(role, 'hosts.write')} />
      </div>
      <HostHeaderActions org={org} host={host} role={role} actions={actions} onSshAccess={onSshAccess} />
    </div>
  );
}

/** The name with a status dot, the state pill and the capability tags.
 * The state is the open alerts' (crit over warn over OK); a host nobody
 * monitors has none, and its pill says so rather than "OK".
 *
 * A client whose agent is offline (backlog 3.57) is off: the pill says so
 * in the neutral tone, with when the agent was last seen and its last
 * state below it, muted — the alerts behind that state are frozen, not
 * current. An online client keeps its state pill and gets a "Client" tag. */
function HostTitle({ org, host, canRead }: { org: string; host: Host; canRead: boolean }) {
  const t = useT();
  const summary = useHostAlertSummary(org, host.id, canRead);
  const agent = useHostAgent(org, host.id);
  const presence = hostPresence(host.kind, agent.data?.agent);
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
  const off = presence.kind === 'off';
  const dotTone: Tone = off ? 'unknown' : tone;
  const dotLabel = off ? t.hosts.presence.off : (label ?? t.hosts.detail.health.unknown);

  return (
    <div className="flex flex-col gap-1">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-1.5">
        <span className="h-2.5 w-2.5 shrink-0 rounded-full" style={{ background: `var(--px-${dotTone})` }} title={dotLabel} />
        <h1 className="min-w-0 font-mono text-xl font-bold break-all">{host.name}</h1>
        {!off && label && <Pill tone={tone}>{label}</Pill>}
        <HostPresenceBadge presence={presence} />
        <span data-testid="host-capabilities" className="flex items-center gap-1.5">
          {host.managed && <Tag className="font-sans">{t.hosts.capability.managed}</Tag>}
          {host.monitored && <Tag className="font-sans">{t.hosts.capability.monitored}</Tag>}
        </span>
      </div>
      {off && presence.kind === 'off' && (
        <div className="flex flex-wrap gap-x-3 text-[13px] text-muted-foreground">
          <span>{t.hosts.presence.lastSeen(seenLabel(presence.lastSeen, new Date(), t))}</span>
          {host.monitored && label && <span>{t.hosts.presence.lastState(label)}</span>}
        </div>
      )}
    </div>
  );
}

/** The header's actions: the two run buttons and the "⋯" menu. Nothing at
 * all for a role that may do none of it. */
function HostHeaderActions({ org, host, role, actions, onSshAccess }: HostHeaderProps) {
  const t = useT();
  const [bootstrapOpen, setBootstrapOpen] = useState(false);
  const test = useRunConnectionTest(org, host.id);
  // SSH access, the connection test and Bootstrap all write the host's SSH
  // side (inventory.write) and only mean something for a managed host.
  const canSsh = host.managed && may(role, 'inventory.write');
  const hasMenu = actions.canEdit || canSsh || actions.canDelete;
  if (!actions.canRun && !hasMenu) return null;

  return (
    <div className="flex shrink-0 flex-wrap items-center gap-2">
      {actions.canRun && (
        <Button variant="primary" onClick={actions.runCommand}>
          <Terminal size={14} /> {t.hosts.detail.runCommand}
        </Button>
      )}
      {actions.canRun && (
        <Button onClick={actions.runTemplate}>
          <FileCode size={14} /> {t.hosts.detail.runTemplate}
        </Button>
      )}
      {hasMenu && (
        <RowMenu
          label={t.hosts.detail.more(host.name)}
          triggerClassName="h-[34px] w-[34px] rounded-md border border-border bg-card text-foreground hover:bg-accent"
        >
          {actions.canEdit && <RowMenuItem onSelect={actions.edit}>{t.hosts.detail.edit}</RowMenuItem>}
          {canSsh && <RowMenuItem onSelect={onSshAccess}>{t.hosts.detail.sshAccess}</RowMenuItem>}
          {canSsh && (
            <RowMenuItem onSelect={() => void test.start()} disabled={test.pending}>
              {t.hosts.detail.startTest}
            </RowMenuItem>
          )}
          {canSsh && <RowMenuItem onSelect={() => setBootstrapOpen(true)}>{t.hosts.detail.bootstrap}</RowMenuItem>}
          {actions.canDelete && (
            <RowMenuItem onSelect={actions.remove} destructive>
              {t.hosts.detail.delete}
            </RowMenuItem>
          )}
        </RowMenu>
      )}
      {bootstrapOpen && <BootstrapDialog org={org} hostId={host.id} hostName={host.name} onClose={() => setBootstrapOpen(false)} />}
    </div>
  );
}
