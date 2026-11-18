import { useState } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { useReloadHost, useZones } from '../../api/hooks/useHosts.ts';
import { useBastions, useHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { HostSshForm } from './HostSshForm.tsx';
import { useHostRoute } from './useHostRoute.ts';

export interface HostSshDialogProps {
  org: string;
  host: Host;
  role: string;
  open: boolean;
  onClose: () => void;
  /** Opens with the key snippets unfolded (the Connection tab's "Show key
   * snippets"). */
  showSnippets?: boolean;
}

/**
 * HostSshDialog is "SSH access · <host>": how Pulsix reaches a managed
 * host — its address, port, user, credential and jump host — with the
 * route that makes, the key to install and a connection test. The caller
 * opens it only for a role with inventory.write (the SSH route's
 * permission). The body is mounted only while it is open, so every opening
 * starts from what is stored.
 */
export function HostSshDialog({ org, host, role, open, onClose, showSnippets = false }: HostSshDialogProps) {
  const t = useT();
  return (
    <Dialog open={open} onClose={onClose} title={t.hosts.sshDialog.title(host.name)} size="wide">
      {open && <HostSshLoader org={org} host={host} role={role} onClose={onClose} showSnippets={showSnippets} />}
    </Dialog>
  );
}

function HostSshLoader({ org, host, role, onClose, showSnippets }: { org: string; host: Host; role: string; onClose: () => void; showSnippets: boolean }) {
  const t = useT();
  const ssh = useHostSSH(org, host.id);
  const bastions = useBastions(org);
  const credentials = useCredentials(org, { enabled: may(role, 'credentials.read') });
  const zones = useZones(org);
  const route = useHostRoute(org, host);
  const reload = useReloadHost(org, host.id);
  // A reload after a 412 remounts the form onto what is stored now; the
  // draft typed before is replaced, never merged into.
  const [formKey, setFormKey] = useState(0);

  if (ssh.isPending) return <p className="text-xs text-muted-foreground">{t.common.loading}</p>;
  if (ssh.isError) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {t.hosts.sshDialog.loadFailed}
      </p>
    );
  }
  return (
    <HostSshForm
      key={formKey}
      org={org}
      host={host}
      current={ssh.data.ssh}
      bastions={bastions.data?.bastions ?? []}
      credentials={(credentials.data?.credentials ?? []).filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
      zones={zones.data?.zones ?? []}
      route={route}
      canEditAddress={may(role, 'hosts.write')}
      canCreateCredential={may(role, 'credentials.write')}
      showSnippets={showSnippets}
      onClose={onClose}
      onReload={async () => {
        await reload();
        setFormKey((k) => k + 1);
      }}
    />
  );
}
