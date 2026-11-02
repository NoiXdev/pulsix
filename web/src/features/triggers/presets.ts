/** PRESETS lists the sender presets (internal/triggers presets); the
 * catalogue names them (`triggers.preset`). */
export const PRESETS = ['pulsix', 'github', 'gitea', 'gitlab', 'bearer'] as const;

export type Preset = (typeof PRESETS)[number];

/** presetLabel names a sender preset, or shows the value itself for one
 * the catalogue does not know (a newer server). */
export function presetLabel(preset: string, labels: Record<string, string>): string {
  return Object.hasOwn(labels, preset) ? labels[preset] : preset;
}
