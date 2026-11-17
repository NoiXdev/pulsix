import { useEffect } from 'react';
import { usePreferences } from '../../api/hooks/usePreferences.ts';
import { readDensityHint, writeDensityHint } from './densityHint.ts';

/**
 * Puts the user's density on the document (`<html data-density="…">`),
 * which DataList's rows read through CSS variables (styles/globals.css).
 * Mounted by RootLayout for a signed-in, unlocked session; unmounting
 * removes the attribute, so a locked page has the default spacing. Until
 * the preferences have loaded (or when they cannot be read) this browser's
 * hint stands in, so the page does not show the default first; once they
 * have, they win and become the new hint. Renders nothing.
 */
export function DensityEffect() {
  const { prefs, loaded } = usePreferences();
  const density = loaded ? prefs.density : readDensityHint();
  useEffect(() => {
    if (!density) return;
    document.documentElement.dataset.density = density;
    if (loaded) writeDensityHint(density);
  }, [density, loaded]);
  useEffect(
    () => () => {
      delete document.documentElement.dataset.density;
    },
    [],
  );
  return null;
}
