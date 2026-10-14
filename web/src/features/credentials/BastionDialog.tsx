import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateBastion, useUpdateBastion } from '../../api/hooks/useSsh.ts';
import type { Bastion, Credential } from '../../api/types.ts';
import { AddCredentialDialog } from './AddCredentialDialog.tsx';
import { KIND_LABEL } from './labels.ts';

export function BastionDialog({
  org, bastion, bastions, credentials, canCreateCredential, onClose, onCreated, initialName,
}: {
  org: string;
  bastion?: Bastion;
  bastions: readonly Bastion[];
  credentials: readonly Credential[];
  /** Gates the Credential field's own create row: credentials.write is
   * admin, stricter than whatever permission let the caller open this
   * dialog in the first place (v0.4g Task 7 fix round — the create prop
   * must not offer an action the signed-in role would get a 403 for). */
  canCreateCredential: boolean;
  onClose: () => void;
  /** Called with the new jump host's id, only on a create (never an edit) — the quick-create row (v0.4g) uses it to select what it just made. */
  onCreated: (id: string) => void;
  /** Prefills the name, from what had been typed into the picker. Same prop shape Task 7 gives AddCredentialDialog and ZoneDialog. */
  initialName?: string;
}) {
  const toast = useToast();
  const create = useCreateBastion(org);
  const update = useUpdateBastion(org, bastion?.id ?? '');
  const [name, setName] = useState(bastion?.name ?? initialName ?? '');
  const [address, setAddress] = useState(bastion?.address ?? '');
  const [port, setPort] = useState(String(bastion?.port ?? 22));
  const [username, setUsername] = useState(bastion?.username ?? '');
  const [credentialId, setCredentialId] = useState(bastion?.credential_id ?? '');
  const [via, setVia] = useState(bastion?.via_bastion_id ?? '');
  const [error, setError] = useState<string | null>(null);
  // null = closed; a string is the name typed into the Credential picker
  // before it opened this.
  const [newCredentialName, setNewCredentialName] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    const body = { name, address, port: Number(port) || 22, username, credential_id: credentialId || undefined, via_bastion_id: via || undefined };
    try {
      if (bastion) {
        await update.mutateAsync(body);
      } else {
        const res = await create.mutateAsync(body);
        onCreated(res.bastion.id);
      }
      toast.show(`Jump host ${name} saved`);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Saving failed');
    }
  }

  return (
    <Dialog open onClose={onClose} title={bastion ? `Jump host ${bastion.name}` : 'Add jump host'}>
      <form onSubmit={submit} className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="bastion-name">Name</Label>
          <Input id="bastion-name" className="font-mono" required value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="grid grid-cols-[1fr_90px] gap-3">
          <div className="flex flex-col gap-1">
            <Label htmlFor="bastion-address">Address</Label>
            <Input id="bastion-address" className="font-mono" required value={address} onChange={(e) => setAddress(e.target.value)} />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="bastion-port">Port</Label>
            <Input id="bastion-port" className="font-mono" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} />
          </div>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="bastion-username">Username</Label>
          <Input id="bastion-username" className="font-mono" required value={username} onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="bastion-credential">Credential</Label>
          <Select
            id="bastion-credential"
            options={credentials
              .filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password')
              .map((c) => ({ value: c.id, label: c.name, hint: KIND_LABEL[c.kind] ?? c.kind }))}
            value={credentialId}
            onChange={setCredentialId}
            placeholder="— none —"
            create={canCreateCredential ? { label: 'New credential', onSelect: setNewCredentialName } : undefined}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="bastion-via">Reached through</Label>
          {/* No create row here: it would open a second BastionDialog on
              top of this one, mid-creation of the jump host it's trying
              to chain through — the same self-nesting the CreateGroupDialog
              Parent picker was refused for (v0.4g Task 5). */}
          <Select
            id="bastion-via"
            options={bastions.filter((b) => b.id !== bastion?.id).map((b) => ({ value: b.id, label: b.name }))}
            value={via}
            onChange={setVia}
            placeholder="— directly —"
          />
        </div>
        {error && (
          <p role="alert" className="text-xs text-destructive">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary">
            {bastion ? 'Save jump host' : 'Create jump host'}
          </Button>
        </div>
      </form>
      {/* Sibling of the <form> above, not inside it (see HostSshCard.tsx's
          own comment on why): a nested <dialog> here would have its own
          submit bubble up into this one's onSubmit too. */}
      {newCredentialName !== null && (
        <AddCredentialDialog
          org={org}
          open
          initialName={newCredentialName}
          onClose={() => setNewCredentialName(null)}
          onCreated={setCredentialId}
        />
      )}
    </Dialog>
  );
}
