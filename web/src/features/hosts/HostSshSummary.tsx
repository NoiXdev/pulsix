import type { ReactNode } from 'react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import type { Host } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { kindName, routeLabel } from '../credentials/labels.ts';
import { PinStateBadge } from './PinStateBadge.tsx';
import { RouteChain } from './RouteChain.tsx';
import { effectiveUser, sshTarget } from './sshDraft.ts';
import { useHostRoute } from './useHostRoute.ts';

const NONE = '—';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-4 border-b border-border py-1.5 last:border-b-0">
      <dt className="shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-right break-words">{children}</dd>
    </div>
  );
}

/**
 * HostSshSummary is the Connection tab's SSH access card: how Pulsix logs
 * in to a managed host — address, port, user, credential — and the route
 * there as a chain, with where it comes from. "Edit …" (for a role with
 * inventory.write) opens the SSH access dialog; without it the card only
 * reads. A role that may not read credentials sees whether one is set, not
 * its name.
 */
export function HostSshSummary({
  org, host, canEdit, canReadCredentials, onEdit,
}: {
  org: string;
  host: Host;
  canEdit: boolean;
  canReadCredentials: boolean;
  onEdit: () => void;
}) {
  const t = useT();
  const c = t.hosts.connectionTab;
  const ssh = useHostSSH(org, host.id);
  const credentials = useCredentials(org, { enabled: canReadCredentials });
  const zones = useZones(org);
  const route = useHostRoute(org, host);
  const s = ssh.data?.ssh;
  const credential = s?.credential_id ? credentials.data?.credentials?.find((x) => x.id === s.credential_id) : undefined;
  const zone = zones.data?.zones?.find((z) => z.id === host.zone_id);
  const address = host.address || host.name;
  const user = effectiveUser(s?.username, credential?.username);

  let credentialText: ReactNode = c.none;
  if (credential) {
    credentialText = (
      <>
        {credential.name} <span className="text-muted-foreground">· {kindName(credential.kind, t)}</span>
      </>
    );
  } else if (s?.credential_id) {
    credentialText = canReadCredentials ? s.credential_id.slice(0, 8) : c.credentialSet;
  }

  return (
    <Card className="p-4">
      <div className="mb-2 flex items-center justify-between gap-3">
        <div className="flex items-center gap-2">
          <h3 className="text-[13px] font-semibold">{c.ssh}</h3>
          <PinStateBadge state={s?.pin_state} />
        </div>
        {canEdit && (
          <Button type="button" onClick={onEdit}>
            {c.edit}
          </Button>
        )}
      </div>
      {!s ? (
        <p className="text-xs text-muted-foreground">{ssh.isError ? t.hosts.sshDialog.loadFailed : t.common.loading}</p>
      ) : (
        <dl className="flex flex-col text-[13px]">
          <Row label={c.address}>
            <span className="font-mono">{address}</span>
          </Row>
          <Row label={c.port}>
            <span className="font-mono">{s.port}</span>
          </Row>
          <Row label={c.user}>{s.username ? <span className="font-mono">{s.username}</span> : <span className="text-muted-foreground">{c.fromCredential}</span>}</Row>
          <Row label={c.credential}>{credentialText}</Row>
          <Row label={c.route}>
            {route ? (
              <span className="inline-flex flex-col items-end gap-0.5" aria-label={t.hosts.ssh.effectiveRoute}>
                <RouteChain worker={t.hosts.sshDialog.worker(zone?.slug ?? '…')} hops={route.names} target={sshTarget(user, address, s.port)} />
                <span className="text-[11px] text-muted-foreground">{routeLabel(route.source, t)}</span>
              </span>
            ) : (
              NONE
            )}
          </Row>
        </dl>
      )}
    </Card>
  );
}
