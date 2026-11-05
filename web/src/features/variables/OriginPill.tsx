import { Pill, type Tone } from '../../ui/badge.tsx';
import type { FieldOrigin } from './types.ts';
import { useT } from '../../i18n/useT.ts';

// Origins are not monitoring states, so only the pill's non-state tones
// and the neutral grey are used; "gone" asks for a person's review.
const TONE: Record<FieldOrigin, Tone> = {
  source: 'accent',
  adjusted: 'maintenance',
  manual: 'unknown',
  gone: 'warn',
};

/** OriginPill says where a template's field came from (spec 7.75 goal 2). */
export function OriginPill({ origin }: { origin: FieldOrigin }) {
  const t = useT();
  return <Pill tone={TONE[origin]}>{t.variables.origins[origin]}</Pill>;
}
