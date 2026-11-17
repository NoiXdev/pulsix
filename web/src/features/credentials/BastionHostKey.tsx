import { Lock } from 'lucide-react';
import { Pill, type Tone } from '../../ui/badge.tsx';
import { CELL_MINOR } from '../../ui/data-list/index.ts';
import type { Bastion } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { PinStateBadge } from '../hosts/PinStateBadge.tsx';
import { bastionKeyState } from './bastionKeyState.ts';

const KEY_STATE_TONE: Record<string, Tone> = { pinned: 'ok', changed: 'crit', unpinned: 'warn' };

/**
 * BastionHostKey is the jump host's TOFU cell (backlog 2.40), mirroring the
 * host card's key block: the pinned fingerprint (or the newest observation),
 * the pin-state badge and what is still to do. Pinning and unpinning are the
 * row menu's: a `changed` key fails every connection through the jump host
 * until the new key is pinned and a fresh connection test confirms it.
 */
export function BastionHostKey({ bastion }: { bastion: Bastion }) {
  const t = useT();
  const state = bastionKeyState(bastion);
  const fingerprint = bastion.ssh_host_key ?? bastion.observed_key ?? '';
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <Lock size={12} className="text-muted-foreground" />
        <span className={`truncate font-mono ${CELL_MINOR}`} title={fingerprint}>
          {fingerprint || t.hosts.connection.noKey}
        </span>
        {state && <Pill tone={KEY_STATE_TONE[state] ?? 'unknown'}>{(t.hosts.connection.keyState as Record<string, string>)[state] ?? state}</Pill>}
        <PinStateBadge state={bastion.pin_state} />
      </div>
      {bastion.pin_state === 'fingerprint_only' && <span className={`text-muted-foreground ${CELL_MINOR}`}>{t.hosts.pin.needsConfirmation}</span>}
      {state === 'changed' && (
        <span className={CELL_MINOR} style={{ color: 'var(--px-crit)' }}>
          {t.credentials.jumpHosts.keyChanged}
        </span>
      )}
    </div>
  );
}
