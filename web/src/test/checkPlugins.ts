import type { CheckPluginDescriptor } from '../api/types.ts';
import fixture from './checkPlugins.json';

/** The descriptors GET /check-plugins serves (checkPlugins.json, which
 * internal/checkplugins regenerates), typed as the API types them. */
export const CHECK_PLUGINS = fixture.plugins as CheckPluginDescriptor[];

/** checkPlugin is one plugin's descriptor out of the fixture. */
export function checkPlugin(name: string): CheckPluginDescriptor {
  const found = CHECK_PLUGINS.find((p) => p.name === name);
  if (!found) throw new Error(`no check plugin ${name} in checkPlugins.json`);
  return found;
}
