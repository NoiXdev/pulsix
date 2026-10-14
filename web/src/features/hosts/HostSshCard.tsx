import { useState, type FormEvent } from 'react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useBastions, useHostSSH, useSetHostSSH } from '../../api/hooks/useSsh.ts';
import { useCredential, useCredentials } from '../../api/hooks/useCredentials.ts';
import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import type { Bastion, Credential, HostSSH } from '../../api/types.ts';
import { AddCredentialDialog } from '../credentials/AddCredentialDialog.tsx';
import { BastionDialog } from '../credentials/BastionDialog.tsx';
import { PublicKeyPanel } from '../credentials/PublicKeyPanel.tsx';
import { KIND_LABEL, routeLabel } from '../credentials/labels.ts';
import { PinStateBadge } from './PinStateBadge.tsx';

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
export function HostSshCard({ org, hostId, managed, canWrite }: { org: string; hostId: string; managed: boolean; canWrite: boolean }) {
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
      current={ssh.data.ssh}
      bastions={bastions.data?.bastions ?? []}
      credentials={(credentials.data?.credentials ?? []).filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')}
      canWrite={canWrite}
    />
  );
}

function HostSshForm({ org, hostId, current, bastions, credentials, canWrite }: { org: string; hostId: string; current: HostSSH; bastions: readonly Bastion[]; credentials: readonly Credential[]; canWrite: boolean }) {
  const toast = useToast();
  const save = useSetHostSSH(org, hostId);
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
        username: username || undefined, port: Number(port) || 22, credential_id: credentialId || undefined,
        bastion_mode: mode, bastion_id: mode === 'explicit' ? bastionId || undefined : undefined,
      });
      toast.show('SSH settings saved');
    } catch (err) {
      toast.show(err instanceof Error ? err.message : 'Saving failed', 'destructive');
    }
  }

  return (
    <Card className="p-4">
      <h2 className="mb-3 text-[13px] font-semibold">SSH access</h2>
      <form onSubmit={submit} className="flex flex-col gap-3">
        <div className="flex items-center gap-2" aria-label="Host key pin state">
          <PinStateBadge state={current.pin_state} />
        </div>
        <div className="grid grid-cols-[1fr_90px] gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-user">User</Label>
            <Input id="ssh-user" className="font-mono" placeholder={selected?.username || 'from the credential'} value={username} onChange={(e) => setUsername(e.target.value)} disabled={!canWrite} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="ssh-port">Port</Label>
            <Input id="ssh-port" className="font-mono" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} disabled={!canWrite} />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="ssh-credential">Credential</Label>
          <Select
            id="ssh-credential"
            options={credentials.map((c) => ({ value: c.id, label: c.name, hint: KIND_LABEL[c.kind] ?? c.kind }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder="— none —"
            disabled={!canWrite}
            create={{ label: 'New credential', onSelect: setNewCredentialName }}
          />
        </div>
        <fieldset className="flex flex-col gap-1">
          <legend className="text-[11px] font-medium text-muted-foreground">Jump host</legend>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'inherit'} onChange={() => setMode('inherit')} disabled={!canWrite} /> Inherit (inventory group, then zone)
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'none'} onChange={() => setMode('none')} disabled={!canWrite} /> Direct connection
          </label>
          <label className="flex items-center gap-2 text-xs">
            <input type="radio" name="ssh-mode" checked={mode === 'explicit'} onChange={() => setMode('explicit')} disabled={!canWrite} /> This jump host
          </label>
          {mode === 'explicit' && (
            <Select
              aria-label="Jump host for this host"
              options={bastions.map((b) => ({ value: b.id, label: b.name }))}
              value={bastionId}
              onChange={setBastionId}
              placeholder="Choose…"
              disabled={!canWrite}
              create={{ label: 'New jump host', onSelect: setNewBastionName }}
            />
          )}
        </fieldset>
        {connection && (
          <p className="text-xs text-muted-foreground" aria-label="Effective route">
            Route: {routeLabel(connection.bastion_source)}
            {names.length > 0 && <span className="font-mono"> · {names.join(' → ')} → host</span>}
          </p>
        )}
        {canWrite && (
          <div className="flex justify-end">
            <Button type="submit" variant="primary" disabled={save.isPending}>
              Save SSH settings
            </Button>
          </div>
        )}
      </form>
      {keyDetail.data?.credential.public_key && (
        <div className="mt-4 border-t border-border pt-4">
          <h3 className="mb-2 text-xs font-semibold">Install this key on the host</h3>
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
          initialName={newBastionName}
          onClose={() => setNewBastionName(null)}
          onCreated={setBastionId}
        />
      )}
    </Card>
  );
}
