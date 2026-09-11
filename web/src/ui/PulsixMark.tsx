/**
 * PulsixMark is the brand mark (brand/logo/pulsix-mark.svg): a hexagon
 * (the host/node) crossed by a pulse line (heartbeat, monitoring), two
 * strokes and two colours — ported inline here rather than referencing the
 * SVG asset so it inherits the app's own CSS variables instead of the
 * brand kit's static fill values.
 */
export function PulsixMark({ size = 22 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 64 64" fill="none" className="shrink-0" aria-hidden="true">
      <path
        d="M32 5 L55 18.5 V45.5 L32 59 L9 45.5 V18.5 Z"
        stroke="var(--px-text)"
        strokeWidth={4}
        strokeLinejoin="round"
      />
      <path
        d="M14 32 H23 L28 20 L35 44 L40 32 H50"
        stroke="var(--px-accent)"
        strokeWidth={4.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
