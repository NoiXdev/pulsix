import { useState } from 'react';
import { useParams } from '@tanstack/react-router';
import { Lock, Server } from 'lucide-react';
import { Card, GridHeader, GridRow } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Select } from '../../ui/Select.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import {
  useBastions, useClearBastionKey, useDeleteBastion, useSetBastionKey, useSetZoneBastion,
} from '../../api/hooks/useSsh.ts';
import { useCredentials } from '../../api/hooks/useCredentials.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { useZones } from '../../api/hooks/useHosts.ts';
import { may } from '../../lib/roles.ts';
import type { Bastion } from '../../api/types.ts';
import { BastionDialog } from './BastionDialog.tsx';
import { PinStateBadge } from '../hosts/PinStateBadge.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';

const COLUMNS = 'minmax(0, 1fr) minmax(0, 1.2fr) 130px 130px minmax(0, 1.6fr) 120px';

const KEY_STATE_TONE: Record<string, Tone> = { pinned: 'ok', changed: 'crit', unpinned: 'warn' };

export function JumpHostsPage() {
  const { org } = useParams({ strict: false }) as { org: string };
  const toast = useToast();
  const bastions = useBastions(org);
  const credentials = useCredentials(org);
  const zones = useZones(org);
  const orgInfo = useOrg(org);
  const remove = useDeleteBastion(org);
  const setZone = useSetZoneBastion(org);
  const [dialog, setDialog] = useState<{ bastion?: Bastion } | null>(null);

  const guard = useQueryGuard(bastions);
  if (guard) return guard;
  if (!bastions.data) return <QueryLoading />;
  const list = bastions.data.bastions ?? [];
  // A viewer may read jump hosts but not credentials; names fall back to "—".
  const creds = credentials.data?.credentials ?? [];
  const bastionName = (id?: string | null) => list.find((b) => b.id === id)?.name ?? 'directly';
  // Pinning is inventory.write, the same gate the host card mirrors.
  const canWrite = may(orgInfo.data?.org.role, 'inventory.write');
  // Gates BastionDialog's own Credential create row: credentials.write is
  // admin, stricter than the inventory.write that gates jump host writes
  // on this page itself (v0.4g Task 7 fix round).
  const canCreateCredential = may(orgInfo.data?.org.role, 'credentials.write');

  return (
    <>
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Jump hosts</h1>
        <Button variant="primary" onClick={() => setDialog({})}>
          <Server size={15} /> Add jump host
        </Button>
      </div>
      <span className="text-[13px] text-muted-foreground">Resolution: host → inventory group → zone default. Chains are rendered as ProxyJump.</span>
      <Card>
        <GridHeader columns={COLUMNS}>
          <span>Name</span>
          <span>Login</span>
          <span>Reached</span>
          <span>Credential</span>
          <span>Host key</span>
          <span />
        </GridHeader>
        {list.map((b) => (
          <GridRow key={b.id} columns={COLUMNS}>
            <span className="font-mono text-[13px]">{b.name}</span>
            <span className="truncate font-mono text-xs">{`${b.username}@${b.address}:${b.port}`}</span>
            <span className="text-xs text-muted-foreground">{b.via_bastion_id ? `via ${bastionName(b.via_bastion_id)}` : 'directly'}</span>
            <span className="truncate font-mono text-xs text-muted-foreground">{creds.find((c) => c.id === b.credential_id)?.name ?? '—'}</span>
            <BastionHostKey org={org} bastion={b} canWrite={canWrite} />
            <span className="flex justify-end gap-1">
              <Button variant="ghost" aria-label={`Edit ${b.name}`} onClick={() => setDialog({ bastion: b })}>
                Edit
              </Button>
              <Button
                variant="ghost"
                aria-label={`Delete ${b.name}`}
                onClick={async () => {
                  if (!confirm(`Delete jump host ${b.name}?`)) return;
                  try {
                    await remove.mutateAsync(b.id);
                    toast.show(`Jump host ${b.name} deleted`);
                  } catch (err) {
                    toast.show(err instanceof Error ? err.message : 'Deleting failed', 'destructive');
                  }
                }}
              >
                Delete
              </Button>
            </span>
          </GridRow>
        ))}
        {list.length === 0 && <p className="px-4 py-6 text-center text-xs text-muted-foreground">No jump hosts yet.</p>}
      </Card>

      <h2 className="text-[15px] font-semibold">Zone defaults</h2>
      <Card>
        {(zones.data?.zones ?? []).map((z) => (
          <GridRow key={z.id} columns="minmax(0, 1fr) 260px">
            <label htmlFor={`zone-${z.id}`} className="text-[13px]">
              {z.name} <span className="font-mono text-xs text-muted-foreground">{z.slug}</span>
            </label>
            {/* No create row: this page's own "Add jump host" button sits
                right above this table — a second way to create one here
                would just duplicate it (v0.4g Move 3). */}
            <Select
              id={`zone-${z.id}`}
              options={list.map((b) => ({ value: b.id, label: b.name }))}
              value={z.bastion_id ?? ''}
              onChange={async (value) => {
                try {
                  await setZone.mutateAsync({ zoneId: z.id, bastionId: value || undefined });
                  toast.show(`Default jump host of ${z.name} saved`);
                } catch (err) {
                  toast.show(err instanceof Error ? err.message : 'Saving failed', 'destructive');
                }
              }}
              placeholder="— direct connection —"
            />
          </GridRow>
        ))}
      </Card>
      {dialog && (
        <BastionDialog
          org={org}
          bastion={dialog.bastion}
          bastions={list}
          credentials={creds}
          canCreateCredential={canCreateCredential}
          onClose={() => setDialog(null)}
          // This page's own list refetches from useCreateBastion's own
          // invalidation; nothing here needs the new id for itself.
          onCreated={() => undefined}
        />
      )}
    </>
  );
}

/**
 * BastionHostKey is the jump host's TOFU cell (backlog 2.40), mirroring the
 * host card's key block: the pinned fingerprint (or the newest observation),
 * the pin-state badge and the pin/unpin actions — a `changed` key fails
 * every connection through the jump host until the new key is pinned and a
 * fresh connection test confirms it.
 */
function BastionHostKey({ org, bastion, canWrite }: { org: string; bastion: Bastion; canWrite: boolean }) {
  const toast = useToast();
  const pin = useSetBastionKey(org, bastion.id);
  const unpin = useClearBastionKey(org, bastion.id);

  const state = bastion.observed_state ?? (bastion.ssh_host_key ? 'pinned' : '');
  const fingerprint = bastion.ssh_host_key ?? bastion.observed_key ?? '';
  return (
    <div className="flex flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Lock size={12} className="text-muted-foreground" />
        <span className="truncate font-mono text-[11px]" title={fingerprint}>
          {fingerprint || 'no key observed'}
        </span>
        {state && <Pill tone={KEY_STATE_TONE[state] ?? 'unknown'}>{state}</Pill>}
        <PinStateBadge state={bastion.pin_state} />
      </div>
      {canWrite && state && state !== 'pinned' && (
        <Button
          type="button"
          variant="ghost"
          className="h-6 px-2 text-[11px]"
          disabled={pin.isPending || !bastion.observed_key}
          aria-label={`Pin ${bastion.name} host key`}
          onClick={async () => {
            if (!bastion.observed_key) return;
            try {
              await pin.mutateAsync(bastion.observed_key);
              toast.show(`Jump host key of ${bastion.name} pinned`);
            } catch (err) {
              toast.show(err instanceof Error ? err.message : 'Pinning failed', 'destructive');
            }
          }}
        >
          Pin
        </Button>
      )}
      {canWrite && state === 'pinned' && (
        <Button
          type="button"
          variant="ghost"
          className="h-6 px-2 text-[11px]"
          disabled={unpin.isPending}
          aria-label={`Unpin ${bastion.name} host key`}
          onClick={async () => {
            try {
              await unpin.mutateAsync();
              toast.show('Jump host pin cleared');
            } catch (err) {
              toast.show(err instanceof Error ? err.message : 'Clearing the pin failed', 'destructive');
            }
          }}
        >
          Unpin
        </Button>
      )}
      {bastion.pin_state === 'fingerprint_only' && (
        <span className="text-[11px] text-muted-foreground">pin needs confirmation</span>
      )}
      {state === 'changed' && (
        <span className="text-[11px]" style={{ color: 'var(--px-crit)' }}>
          key changed — connections through it are blocked
        </span>
      )}
    </div>
  );
}
