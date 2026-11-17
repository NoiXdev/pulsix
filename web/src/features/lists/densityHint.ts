import type { Density } from '../../api/types.ts';

const KEY = 'pulsix.density';
const DENSITIES: readonly string[] = ['comfortable', 'standard', 'compact'];

/**
 * The density this browser last knew for its user — a hint, not the
 * setting: the server's preferences stay the source of truth. It lets the
 * first paint of a full page load already have the user's spacing
 * (applyDensityHint, called by main.tsx before React renders) instead of
 * jumping once the preferences answer. Storage may be missing or refuse
 * access (private mode, blocked site data): every access is guarded, and
 * without it the page simply starts at the default.
 */
export function readDensityHint(): Density | undefined {
  try {
    const v = window.localStorage.getItem(KEY);
    return v !== null && DENSITIES.includes(v) ? (v as Density) : undefined;
  } catch {
    return undefined;
  }
}

export function writeDensityHint(density: Density): void {
  try {
    window.localStorage.setItem(KEY, density);
  } catch {
    // A hint only: without storage the next load starts at the default.
  }
}

/** Puts the hinted density on the document, if there is one. */
export function applyDensityHint(): void {
  const hint = readDensityHint();
  if (hint) document.documentElement.dataset.density = hint;
}
