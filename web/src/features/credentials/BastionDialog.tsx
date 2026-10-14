import { useState, type FormEvent } from 'react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useCreateBastion, useUpdateBastion } from '../../api/hooks/useSsh.ts';
import type { Bastion, Credential } from '../../api/types.ts';

const SELECT = 'h-8 w-full rounded-md border border-border bg-background px-2 text-xs text-foreground';

export function BastionDialog({
  org, bastion, bastions, credentials, onClose, onCreated,
}: {
  org: string;
  bastion?: Bastion;
  bastions: readonly Bastion[];
  credentials: readonly Credential[];
  onClose: () => void;
  /** Called with the new jump host's id, only on a create (never an edit) — the quick-create row (v0.4g) uses it to select what it just made. */
  onCreated: (id: string) => void;
}) {
  const toast = useToast();
  const create = useCreateBastion(org);
  const update = useUpdateBastion(org, bastion?.id ?? '');
  const [name, setName] = useState(bastion?.name ?? '');
  const [address, setAddress] = useState(bastion?.address ?? '');
  const [port, setPort] = useState(String(bastion?.port ?? 22));
  const [username, setUsername] = useState(bastion?.username ?? '');
  const [credentialId, setCredentialId] = useState(bastion?.credential_id ?? '');
  const [via, setVia] = useState(bastion?.via_bastion_id ?? '');
  const [error, setError] = useState<string | null>(null);

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
          <select id="bastion-credential" className={SELECT} value={credentialId} onChange={(e) => setCredentialId(e.target.value)}>
            <option value="">— none —</option>
            {credentials.filter((c) => c.kind === 'ssh_key' || c.kind === 'ssh_password').map((c) => (
              <option key={c.id} value={c.id}>
                {c.name}
              </option>
            ))}
          </select>
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="bastion-via">Reached through</Label>
          <select id="bastion-via" className={SELECT} value={via} onChange={(e) => setVia(e.target.value)}>
            <option value="">— directly —</option>
            {bastions.filter((b) => b.id !== bastion?.id).map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
              </option>
            ))}
          </select>
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
    </Dialog>
  );
}
