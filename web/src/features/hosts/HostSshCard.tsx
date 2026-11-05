import { useState, type FormEvent } from 'react';
import { ApiError } from '../../api/client.ts';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useReloadHost } from '../../api/hooks/useHosts.ts';
import { useBastions, useHostSSH, useSetHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredential, useCredentials } from '../../api/hooks/useCredentials.ts';
import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import type { Bastion, Credential, HostSSH } from '../../api/types.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { BastionDialog } from '../credentials/BastionDialog.tsx';
import { PublicKeyPanel } from '../credentials/PublicKeyPanel.tsx';
import { kindName, routeLabel } from '../credentials/labels.ts';
import { PinStateBadge } from './PinStateBadge.tsx';
import { StaleNotice } from './StaleNotice.tsx';
import { useT } from '../../i18n/useT.ts';

/** HostSshCard is the SSH access step of the enrollment mockup for a
 * managed host: how Pulsix logs in, which jump host it goes through (and
 * why), and the public key to install. Read-only for a viewer (backlog
 * 2.18): `canWrite` mirrors the server's `inventory.write` permission —
 * the same permission PUT .../hosts/{id}/ssh requires — from the caller's
 * org role, the same way ConnectionTestCard and HostDetailPage already
 * gate their own inventory writes. The SPA session carries only the org
 * role, not a permission list (see lib/roles.ts's doc comment), so this
 * mirrors the role→permission matrix client-side rather than reading a
 * permission list that does not exist. */
export function HostSshCard({
  org, hostId, revision, managed, canWrite, canCreateCredential,
}: {
  org: string;
  hostId: string;
  /** The host's revision, sent as If-Match with a save (a 412 offers a reload). */
  revision: number;
  managed: boolean;
  canWrite: boolean;
  /** Gates this card's own Credential create row, its Jump host create row
   * (POST /bastions is credentials.write too — a jump host is a credential
   * holder, see sshconf_handlers.go), and — forwarded — the Credential
   * create row nested inside the Jump host row's own BastionDialog:
   * credentials.write is admin, stricter than the inventory.write that
   * gates `canWrite` itself (v0.4g Task 7 and 10 fix rounds). */
  canCreateCredential: boolean;
}) {
  const ssh = useHostSSH(org, managed ? hostId : '');
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  if (!managed) return null;
  if (!ssh.data) return null;
  return (
    <HostSshForm
      key={JSON.stringify(ssh.data.ssh)}
      org={org}
      hostId={hostId}
      revision={revision}
      current={ssh.data.ssh}
      bastions={bastions.data?.bastions ?? []}
      credentials={(credentials.data?.credentials ?? []).filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
      canWrite={canWrite}
      canCreateCredential={canCreateCredential}
    />
  );
}

function HostSshForm({
  org, hostId, revision, current, bastions, credentials, canWrite, canCreateCredential,
}: {
  org: string;
  hostId: string;
  revision: number;
  current: HostSSH;
  bastions: readonly Bastion[];
  credentials: readonly Credential[];
  canWrite: boolean;
  canCreateCredential: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const save = useSetHostSSH(org, hostId);
  const reloadHost = useReloadHost(org, hostId);
  const [stale, setStale] = useState(false);
  const preview = useHostInventoryPreview(org, hostId);
  const [username, setUsername] = useState(current.username);
  const [port, setPort] = useState(String(current.port));
  const [credentialId, setCredentialId] = useState(current.credential_id ?? '');
  const [mode, setMode] = useState(current.bastion_mode);
  const [bastionId, setBastionId] = useState(current.bastion_id ?? '');
  // null = closed; a string is the name typed into the picker before it
  // opened this, as BastionDialog/AddCredentialDialog's initialName expects.
  const [newCredentialName, setNewCredentialName] = useState<string | null>(null);
  const [newBastionName, setNewBastionName] = useState<string | null>(null);
  const selected = credentials.find((c) => c.id === credentialId);
  const keyDetail = useCredential(org, selected?.kind === 'ssh_key' ? selected.id : '');
  const connection = preview.data?.connection;
  const names = (connection?.bastions ?? []).map((id) => bastions.find((b) => b.id === id)?.name ?? id.slice(0, 8));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!canWrite) return;
    try {
      await save.mutateAsync({
        body: {
          username: username || undefined, port: Number(port) || 22, credential_id: credentialId || undefined,
          bastion_mode: mode, bastion_id: mode === 'explicit' ? bastionId || undefined : undefined,
        },
        revision,
      });
      setStale(false);
      toast.show(t.hosts.ssh.saved);
    } catch (err) {
      if (err instanceof ApiError && err.status === 412) {
        setStale(true);
        return;
      }
      toast.show(err instanceof Error ? err.message : t.hosts.ssh.saveFailed, 'destructive');
    }
  }

  async function reload() {
    await reloadHost();
    setStale(false);
  }

  return (
    <Card className="p-4">
      <h2 className="mb-3 text-[13px] font-semibold">{t.hosts.ssh.title}</h2>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="flex items-center gap-2" aria-label={t.hosts.ssh.pinState}>
          <PinStateBadge state={current.pin_state} />
        </div>
        <div className="grid grid-cols-[1fr_90px] gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-user">{t.hosts.ssh.user}</Label>
            <Input id="ssh-user" className="font-mono" placeholder={selected?.username || t.hosts.ssh.userPlaceholder} value={username} onChange={(e) => setUsername(e.target.value)} disabled={!canWrite} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-port">{t.hosts.ssh.port}</Label>
            <Input id="ssh-port" className="font-mono" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} disabled={!canWrite} />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="ssh-credential">{t.hosts.ssh.credential}</Label>
          <Select
            id="ssh-credential"
            options={credentials.map((c) => ({ value: c.id, label: c.name, hint: kindName(c.kind, t) }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder={t.hosts.ssh.noCredential}
            disabled={!canWrite}
            create={canCreateCredential ? { label: t.hosts.ssh.newCredential, onSelect: setNewCredentialName } : undefined}
          />
        </div>
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">{t.hosts.ssh.jumpHost}</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'inherit'} onChange={() => setMode('inherit')} disabled={!canWrite} /> {t.hosts.ssh.inherit}
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'none'} onChange={() => setMode('none')} disabled={!canWrite} /> {t.hosts.ssh.direct}
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'explicit'} onChange={() => setMode('explicit')} disabled={!canWrite} /> {t.hosts.ssh.explicit}
          </label>
          {mode === 'explicit' && (
            <Select
              aria-label={t.hosts.ssh.jumpHostFor}
              options={bastions.map((b) => ({ value: b.id, label: b.name }))}
              value={bastionId}
              onChange={setBastionId}
              placeholder={t.hosts.ssh.choose}
              disabled={!canWrite}
              create={canCreateCredential ? { label: t.hosts.ssh.newJumpHost, onSelect: setNewBastionName } : undefined}
            />
          )}
        </fieldset>
        {connection && (
          <p className="text-xs text-muted-foreground" aria-label={t.hosts.ssh.effectiveRoute}>
            {t.hosts.ssh.route} {routeLabel(connection.bastion_source, t)}
            {names.length > 0 && <span className="font-mono">{t.hosts.ssh.routeVia(names.join(' → '))}</span>}
          </p>
        )}
        {stale && <StaleNotice onReload={reload} />}
        {canWrite && (
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              {t.hosts.ssh.submit}
            </Button>
          </div>
        )}
      </form>
      {keyDetail.data?.credential.public_key && (
        <div className="mt-4 border-t border-border pt-4">
          <h3 className="mb-2 text-xs font-semibold">{t.hosts.ssh.installKey}</h3>
          <PublicKeyPanel publicKey={keyDetail.data.credential.public_key} snippets={keyDetail.data.snippets} />
        </div>
      )}
      {/* Siblings of the <form> above, not inside it: ui/dialog.tsx does not
          portal, so a <dialog> nested inside this card's own <form> would
          have its own submit bubble up into this form's onSubmit too. */}
      {newCredentialName !== null && (
        <AddCredentialDialog
          org={org}
          open
          initialName={newCredentialName}
          onClose={() => setNewCredentialName(null)}
          onCreated={setCredentialId}
        />
      )}
      {newBastionName !== null && (
        <BastionDialog
          org={org}
          bastions={bastions}
          credentials={credentials}
          canCreateCredential={canCreateCredential}
          initialName={newBastionName}
          onClose={() => setNewBastionName(null)}
          onCreated={setBastionId}
        />
      )}
    </Card>
  );
}
