import { Pill, type Tone } from '../../ui/badge.tsx';

/**
 * PinStateBadge is the host-key pin state badge (backlog 2.43): a
 * fingerprint-only pin — one made before full host keys were stored — is
 * not enforceable until the next connection test observes the key and
 * completes the pin (the daily re-test does it within a day), so the cards
 * say "pin needs confirmation" instead of silently reading as safe.
 * Nothing renders while the host is unpinned: trust-on-first-use is not a
 * pin state.
 */
const PIN_BADGE: Record<string, { label: string; tone: Tone }> = {
  full: { label: 'pin complete', tone: 'ok' },
  fingerprint_only: { label: 'pin needs confirmation', tone: 'warn' },
};

export function PinStateBadge({ state }: { state?: string | null }) {
  const badge = state ? PIN_BADGE[state] : undefined;
  if (!badge) return null;
  return (
    <Pill tone={badge.tone}>{badge.label}</Pill>
  );
}
