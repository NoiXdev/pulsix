import { useNavigate } from '@tanstack/react-router';
import { Activity, Lock } from 'lucide-react';
import { Card } from '../../ui/table.tsx';
import { Button } from '../../ui/button.tsx';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useClearHostKey, useHostTest, useSetHostKey, useStartConnectionTest } from '../../api/hooks/useHostOps.ts';

const KEY_STATE_TONE: Record<string, Tone> = { pinned: 'ok', changed: 'crit', unpinned: 'warn' };

/**
 * ConnectionTestCard is the host's TOFU panel (spec §5, backlog 2.7c): the
 * operator starts a connection test (a connection_test job), the latest
 * result's checks read row by row in the state colours, and the observed
 * host key fingerprint is pinned or cleared — a `changed` key blocks jobs
 * until the new key is pinned and a new connection test confirms it. Read-only for viewers
 * (inventory.write is an operator permission server-side; `canWrite`
 * mirrors that from the session's role). The host detail DTO does not
 * carry the test state (Task 10 deferral): the card reads it here.
 */
export function ConnectionTestCard({ org, hostId, canWrite }: { org: string; hostId: string; canWrite: boolean }) {
  const test = useHostTest(org, hostId);
  if (!canWrite && test.isPending) return null;
  return (
    <Card>
      <div className="flex flex-col gap-3 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-semibold">Connection test</h2>
          {canWrite && <RunTestButton org={org} hostId={hostId} />}
        </div>
        {test.isPending && <p className="text-xs text-muted-foreground">Loading…</p>}
        {test.isError && <p className="text-xs text-muted-foreground">No connection test yet.</p>}
        {test.data && <TestResult org={org} hostId={hostId} test={test.data.test} canWrite={canWrite} />}
      </div>
    </Card>
  );
}

function RunTestButton({ org, hostId }: { org: string; hostId: string }) {
  const toast = useToast();
  const start = useStartConnectionTest(org, hostId);
  const navigate = useNavigate();
  return (
    <Button
      type="button"
      disabled={start.isPending}
      onClick={async () => {
        try {
          const { job_id } = await start.mutateAsync();
          toast.show('Connection test started');
          await navigate({ to: '/o/$org/runs/$id', params: { org, id: job_id } });
        } catch (err) {
          toast.show(err instanceof Error ? err.message : 'Starting the test failed', 'destructive');
        }
      }}
    >
      <Activity size={13} /> Run connection test
    </Button>
  );
}

function TestResult({
  org, hostId, test, canWrite,
}: {
  org: string;
  hostId: string;
  test: {
    at: string;
    ok: boolean;
    host_key: string;
    host_key_state: string;
    checks: { name: string; detail: string; ok: boolean }[] | null;
  };
  canWrite: boolean;
}) {
  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2">
        <Pill tone={test.ok ? 'ok' : 'crit'}>{test.ok ? 'ok' : 'failed'}</Pill>
        <span className="text-[11px] text-muted-foreground">{test.at.slice(0, 19).replace('T', ' ')}</span>
      </div>
      <ul className="flex flex-col">
        {(test.checks ?? []).map((c) => (
          <li key={c.name} className="flex items-center justify-between gap-3 border-b border-border py-1.5 text-xs last:border-b-0">
            <span className="font-mono">{c.name}</span>
            <span className="truncate" style={{ color: c.ok ? 'var(--px-ok)' : 'var(--px-crit)' }}>
              {c.detail}
            </span>
          </li>
        ))}
      </ul>
      <HostKey org={org} hostId={hostId} fingerprint={test.host_key} state={test.host_key_state} canWrite={canWrite} />
    </div>
  );
}

function HostKey({
  org, hostId, fingerprint, state, canWrite,
}: {
  org: string;
  hostId: string;
  fingerprint: string;
  state: string;
  canWrite: boolean;
}) {
  const tone = KEY_STATE_TONE[state] ?? 'unknown';
  return (
    <div className="flex flex-col gap-1.5 rounded-md border border-border bg-background p-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <Lock size={13} className="text-muted-foreground" />
        <span className="font-mono text-[11px]">{fingerprint || 'no key observed'}</span>
        <Pill tone={tone}>{state}</Pill>
        {canWrite && state !== 'pinned' && <PinButton org={org} hostId={hostId} fingerprint={fingerprint} />}
        {canWrite && state === 'pinned' && <UnpinButton org={org} hostId={hostId} />}
      </div>
      {state === 'changed' && (
        <p className="text-[11px]" style={{ color: 'var(--px-crit)' }}>
          The host key changed since it was pinned — jobs are blocked until the new key is pinned and a new connection test confirms it.
        </p>
      )}
      {state === 'unpinned' && <p className="text-[11px] text-muted-foreground">Pinning the observed key enables trust-on-first-use.</p>}
    </div>
  );
}

function PinButton({ org, hostId, fingerprint }: { org: string; hostId: string; fingerprint: string }) {
  const toast = useToast();
  const pin = useSetHostKey(org, hostId);
  return (
    <Button
      type="button"
      disabled={pin.isPending || !fingerprint}
      onClick={async () => {
        try {
          await pin.mutateAsync(fingerprint);
          toast.show('Host key pinned');
        } catch (err) {
          toast.show(err instanceof Error ? err.message : 'Pinning failed', 'destructive');
        }
      }}
    >
      Pin host key
    </Button>
  );
}

function UnpinButton({ org, hostId }: { org: string; hostId: string }) {
  const toast = useToast();
  const unpin = useClearHostKey(org, hostId);
  return (
    <Button
      type="button"
      variant="ghost"
      disabled={unpin.isPending}
      onClick={async () => {
        try {
          await unpin.mutateAsync();
          toast.show('Host key pin cleared');
        } catch (err) {
          toast.show(err instanceof Error ? err.message : 'Clearing the pin failed', 'destructive');
        }
      }}
    >
      Unpin
    </Button>
  );
}
