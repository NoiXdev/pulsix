import { Pill, type Tone } from '../../ui/badge.tsx';
import { useT } from '../../i18n/useT.ts';

/**
 * PinStateBadge is the host-key pin state badge (backlog 2.43): a
 * fingerprint-only pin — one made before full host keys were stored — is
 * not enforceable until the next connection test observes the key and
 * completes the pin (the daily re-test does it within a day), so the cards
 * say "pin needs confirmation" instead of silently reading as safe.
 * Nothing renders while the host is unpinned: trust-on-first-use is not a
 * pin state.
 */
const PIN_TONE: Record<string, Tone> = { full: 'ok', fingerprint_only: 'warn' };

export function PinStateBadge({ state }: { state?: string | null }) {
  const t = useT();
  const tone = state ? PIN_TONE[state] : undefined;
  if (!tone) return null;
  return <Pill tone={tone}>{state === 'full' ? t.hosts.pin.complete : t.hosts.pin.needsConfirmation}</Pill>;
}
