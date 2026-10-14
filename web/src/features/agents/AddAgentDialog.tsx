import { useState, type FormEvent } from 'react';
import { Copy } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useCreateAgentToken } from '../../api/hooks/useAgents.ts';
import { ZoneDialog } from '../hosts/ZoneDialog.tsx';

const NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

interface Created {
  token: string;
  command: string;
}

/** AddAgentDialog creates an enrollment token and shows it once, with the
 * one-line install command that uses it. A token may admit several agents
 * (a fleet rollout) and expires on its own; the host it creates lands in
 * the chosen zone, named by the hint or by the agent itself. */
export function AddAgentDialog({ org, onClose }: { org: string; onClose: () => void }) {
  const zones = useZones(org);
  const create = useCreateAgentToken(org);
  const [zoneId, setZoneId] = useState('');
  const [nameHint, setNameHint] = useState('');
  const [maxUses, setMaxUses] = useState(1);
  const [minutes, setMinutes] = useState(60);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<Created | null>(null);
  const [zoneDialogName, setZoneDialogName] = useState<string | null>(null);
  const zoneList = zones.data?.zones ?? [];
  const zone = zoneId || zoneList[0]?.id || '';

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (nameHint && !NAME_RE.test(nameHint)) {
      setError('Use lowercase letters, digits and hyphens, starting with a letter or digit.');
      return;
    }
    try {
      const res = await create.mutateAsync({
        zone_id: zone,
        max_uses: maxUses,
        expires_in_minutes: minutes,
        ...(nameHint ? { name_hint: nameHint } : {}),
      });
      setCreated({ token: res.token, command: res.install_command });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Creating the token failed');
    }
  }

  if (created) {
    return (
      <Dialog open onClose={onClose} title="Enrol an agent" size="wide">
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted-foreground">
            Run this on the host as root. The agent downloads itself from this manager, enrols with the token and appears here. The token is shown only now
            — it admits {maxUses === 1 ? 'one agent' : `${maxUses} agents`} and expires in {minutes} minutes.
          </p>
          <pre className="overflow-x-auto rounded-md border border-border bg-background p-3 font-mono text-[11px]" aria-label="Install command">
            {created.command}
          </pre>
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs" aria-label="Enrollment token">
              {created.token}
            </code>
            <Button type="button" variant="ghost" aria-label="Copy token" onClick={() => void navigator.clipboard.writeText(created.token)}>
              <Copy size={13} />
            </Button>
          </div>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" aria-label="Copy command" onClick={() => void navigator.clipboard.writeText(created.command)}>
              <Copy size={13} /> Copy
            </Button>
            <Button type="button" variant="primary" onClick={onClose}>
              Done
            </Button>
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title="Enrol an agent">
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="agent-zone">Zone</Label>
        <Select
          id="agent-zone"
          options={zoneList.map((z) => ({ value: z.id, label: `${z.name} (${z.slug})` }))}
          value={zone}
          onChange={setZoneId}
          create={{ label: 'New zone…', onSelect: setZoneDialogName }}
        />
        <Label htmlFor="agent-name">Host name (optional)</Label>
        <Input
          id="agent-name"
          className="font-mono"
          value={nameHint}
          onChange={(e) => setNameHint(e.target.value)}
          placeholder="the agent's own host name"
        />
        <Label htmlFor="agent-uses">Agents this token may enrol</Label>
        <Input id="agent-uses" type="number" min={1} max={100} value={maxUses} onChange={(e) => setMaxUses(Number(e.target.value))} />
        <Label htmlFor="agent-minutes">Valid for (minutes)</Label>
        <Input id="agent-minutes" type="number" min={1} max={10080} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))} />
        {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || !zone}>
            Create token
          </Button>
        </div>
      </form>
      {zoneDialogName !== null && (
        <ZoneDialog
          org={org}
          open
          onClose={() => setZoneDialogName(null)}
          initialName={zoneDialogName}
          onCreated={(id) => setZoneId(id)}
        />
      )}
    </Dialog>
  );
}
