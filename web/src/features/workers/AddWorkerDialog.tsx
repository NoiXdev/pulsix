import { useState, type FormEvent } from 'react';
import { Copy } from 'lucide-react';
import { Dialog } from '../../ui/dialog.tsx';
import { Button } from '../../ui/button.tsx';
import { Input } from '../../ui/input.tsx';
import { Label } from '../../ui/label.tsx';
import { Select } from '../../ui/Select.tsx';
import { useZones } from '../../api/hooks/useHosts.ts';
import { useCreateWorkerEnrollment } from '../../api/hooks/useWorkers.ts';
import { ZoneDialog } from '../hosts/ZoneDialog.tsx';
import { useT } from '../../i18n/useT.ts';

const NAME_RE = /^[a-z0-9][a-z0-9-]{0,62}$/;

interface Created {
  token: string;
  docker: string;
  compose: string;
}

/** AddWorkerDialog creates a one-time enrollment token for a new worker
 * (zone + name) and shows it once, with the docker run and compose
 * snippets that start a worker enrolling with it. */
export function AddWorkerDialog({ org, onClose }: { org: string; onClose: () => void }) {
  const t = useT();
  const zones = useZones(org);
  const create = useCreateWorkerEnrollment(org);
  const [zoneId, setZoneId] = useState('');
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState<Created | null>(null);
  const [tab, setTab] = useState<'docker' | 'compose'>('docker');
  const [zoneDialogName, setZoneDialogName] = useState<string | null>(null);
  const zoneList = zones.data?.zones ?? [];
  const zone = zoneId || zoneList[0]?.id || '';

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (!NAME_RE.test(name)) {
      setError(t.workers.dialog.nameRule);
      return;
    }
    try {
      const res = await create.mutateAsync({ zone_id: zone, name });
      setCreated({ token: res.token, docker: res.docker_snippet, compose: res.compose_snippet });
    } catch (err) {
      setError(err instanceof Error ? err.message : t.workers.dialog.createFailed);
    }
  }

  if (created) {
    const snippet = tab === 'docker' ? created.docker : created.compose;
    return (
      <Dialog open onClose={onClose} title={t.workers.dialog.titleNamed(name)} size="wide">
        <div className="flex flex-col gap-4">
          <p className="text-xs text-muted-foreground">
            {t.workers.dialog.tokenNote}
          </p>
          <div className="flex items-center gap-2">
            <code className="flex-1 truncate rounded-md border border-border bg-background px-2 py-1.5 font-mono text-xs" aria-label={t.workers.dialog.tokenLabel}>
              {created.token}
            </code>
            <Button type="button" variant="ghost" aria-label={t.workers.dialog.copyToken} onClick={() => void navigator.clipboard.writeText(created.token)}>
              <Copy size={13} />
            </Button>
          </div>
          <div role="tablist" className="flex gap-4 border-b border-border">
            {(['docker', 'compose'] as const).map((k) => (
              <button
                key={k}
                type="button"
                role="tab"
                aria-selected={tab === k}
                className={`border-b-2 py-1.5 text-xs ${tab === k ? 'border-primary font-semibold' : 'border-transparent text-muted-foreground'}`}
                onClick={() => setTab(k)}
              >
                {k === 'docker' ? t.workers.dialog.docker : t.workers.dialog.compose}
              </button>
            ))}
          </div>
          <pre className="overflow-x-auto rounded-md border border-border bg-background p-3 font-mono text-[11px]" aria-label={t.workers.dialog.snippetLabel}>
            {snippet}
          </pre>
          <div className="flex justify-end gap-2">
            <Button type="button" variant="ghost" aria-label={t.workers.dialog.copySnippet} onClick={() => void navigator.clipboard.writeText(snippet)}>
              <Copy size={13} /> {t.workers.dialog.copy}
            </Button>
            <Button type="button" variant="primary" onClick={onClose}>
              {t.workers.dialog.done}
            </Button>
          </div>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open onClose={onClose} title={t.workers.dialog.title}>
      <form className="flex flex-col gap-3" onSubmit={(e) => void submit(e)}>
        <Label htmlFor="worker-zone">{t.workers.dialog.zone}</Label>
        <Select
          id="worker-zone"
          options={zoneList.map((z) => ({ value: z.id, label: `${z.name} (${z.slug})` }))}
          value={zone}
          onChange={setZoneId}
          create={{ label: t.workers.dialog.newZone, onSelect: setZoneDialogName }}
        />
        <Label htmlFor="worker-name">{t.workers.dialog.name}</Label>
        <Input id="worker-name" className="font-mono" value={name} onChange={(e) => setName(e.target.value)} placeholder={t.workers.dialog.namePlaceholder} />
        {error && <p className="text-xs text-[var(--px-crit)]">{error}</p>}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={onClose}>
            {t.common.cancel}
          </Button>
          <Button type="submit" variant="primary" disabled={create.isPending || !zone}>
            {t.workers.dialog.createToken}
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
