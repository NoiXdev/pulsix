import { useState } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { useReloadHost, useZones } from '../../api/hooks/useHosts.ts';
import { useBastions, useHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useHostTags } from '../../api/hooks/useTags.ts';
import type { Host } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { manualTagPairs } from './tags.ts';
import { HostForm, INHERIT } from './HostForm.tsx';

export interface HostDialogProps {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Edit this host; add a new one when unset. */
  host?: Host;
}

/**
 * HostDialog adds a host or edits one — the same fields either way: name,
 * address, zone, jump host, tags and the two capabilities. The jump host
 * and the tags travel in the same request as the host itself, so a new host
 * is never saved half-configured.
 *
 * The body is mounted only while the dialog is open (ui/dialog.tsx keeps its
 * children mounted), so every opening starts from a fresh form and no query
 * below runs while it is closed.
 */
export function HostDialog({ org, open, onOpenChange, host }: HostDialogProps) {
  const t = useT();
  const close = () => onOpenChange(false);
  return (
    <Dialog open={open} onClose={close} title={host ? t.hosts.dialog.titleEdit(host.name) : t.hosts.dialog.titleAdd}>
      {open && <HostDialogLoader org={org} host={host} onClose={close} />}
    </Dialog>
  );
}

/** HostDialogLoader gathers what the form pre-fills from — the caller's role
 * (it decides whether the jump host is offered at all), and, when editing,
 * the host's tags and SSH settings — and mounts the form once it has them,
 * so the form's initial values are never a half-loaded guess. */
function HostDialogLoader({ org, host, onClose }: { org: string; host?: Host; onClose: () => void }) {
  const t = useT();
  const orgQuery = useOrg(org);
  const role = orgQuery.data?.org.role;
  const canJump = may(role, 'inventory.write');
  const canCreateBastion = may(role, 'credentials.write');
  const hostId = host?.id ?? '';
  const editId = canJump ? hostId : '';

  const zones = useZones(org);
  const bastions = useBastions(org);
  const credentials = useCredentials(org, { enabled: canCreateBastion });
  const tags = useHostTags(org, hostId);
  const ssh = useHostSSH(org, editId);
  const preview = useHostInventoryPreview(org, editId);
  const reload = useReloadHost(org, hostId);

  // The host the form edits, and the key that remounts the form onto a
  // reloaded one. A stale host is replaced, never merged into.
  const [base, setBase] = useState(host);
  const [formKey, setFormKey] = useState(0);

  async function onReload(): Promise<void> {
    setBase(await reload());
    setFormKey((k) => k + 1);
  }

  // orgQuery's failure is not fatal: no role means no jump host field, and
  // the server still has the last word on every write.
  const pending =
    orgQuery.isPending || zones.isPending || (!!host && (tags.isPending || (canJump && ssh.isPending)));
  if (pending) return <p className="text-xs text-muted-foreground">{t.common.loading}</p>;
  if (zones.isError || (host && (tags.isError || (canJump && ssh.isError)))) {
    return (
      <p role="alert" className="text-xs text-destructive">
        {t.hosts.dialog.loadFailed}
      </p>
    );
  }

  const manualTags = manualTagPairs(tags.data?.tags);
  const sshInitial = ssh.data?.ssh;
  const initialJump = sshInitial?.bastion_mode === 'explicit' && sshInitial.bastion_id ? sshInitial.bastion_id : (sshInitial?.bastion_mode ?? INHERIT);

  return (
    <HostForm
      key={formKey}
      org={org}
      host={base}
      onClose={onClose}
      onReload={onReload}
      canJump={canJump}
      canCreateBastion={canCreateBastion}
      zones={zones.data?.zones ?? []}
      bastions={bastions.data?.bastions ?? []}
      credentials={credentials.data?.credentials ?? []}
      initialTags={manualTags}
      initialJump={initialJump}
      // A connection with a null chain is a resolved direct route, not an unknown one.
      resolvedChain={preview.data?.connection ? (preview.data.connection.bastions ?? []) : undefined}
    />
  );
}
