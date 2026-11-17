import { useEffect } from 'react';
import { usePreferences } from '../../api/hooks/usePreferences.ts';

/**
 * Puts the user's density on the document (`<html data-density="…">`),
 * which DataList's rows read through CSS variables (styles/globals.css).
 * Mounted by RootLayout for a signed-in, unlocked session; unmounting
 * removes the attribute, so a locked or signed-out page has the default
 * spacing. Renders nothing.
 */
export function DensityEffect() {
  const { prefs } = usePreferences();
  const density = prefs.density;
  useEffect(() => {
    document.documentElement.dataset.density = density;
  }, [density]);
  useEffect(
    () => () => {
      delete document.documentElement.dataset.density;
    },
    [],
  );
  return null;
}
