import type { Messages } from '../../i18n/types.ts';

/**
 * The temperature, smart and raid plugins (backlog 3.9) each take one
 * optional list of strings, which an organization's check dialog and the
 * instance's shared check form edit as a multi-line textarea like disk's
 * mounts. validateHardwareParams in internal/checks is the authority; the
 * rules here only keep a bad entry from being sent.
 */
export const HARDWARE_PLUGINS = ['temperature', 'smart', 'raid'] as const;
export type HardwarePlugin = (typeof HARDWARE_PLUGINS)[number];

export function isHardwarePlugin(plugin: string): plugin is HardwarePlugin {
  return (HARDWARE_PLUGINS as readonly string[]).includes(plugin);
}

/** The API parameter each plugin's list travels in. */
export const HARDWARE_PARAM: Record<HardwarePlugin, 'sensors' | 'devices' | 'arrays'> = {
  temperature: 'sensors',
  smart: 'devices',
  raid: 'arrays',
};

const MAX_ENTRIES = 64;
const MAX_BYTES = 255;
const DEVICE = /^\/dev\/[A-Za-z0-9._/,:+@-]+$/;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f-\u009f]/;
const byteLen = (s: string) => new TextEncoder().encode(s).length;

/** entries turns the textarea into the list the API takes: trimmed lines,
 * blank ones dropped. */
function entries(text: string): string[] {
  return text.split('\n').map((l) => l.trim()).filter(Boolean);
}

/** hardwareParams builds the params a save sends: an empty list is `{}`
 * (every sensor, disk or array), otherwise `{param: [...]}`. */
export function hardwareParams(plugin: HardwarePlugin, text: string): Record<string, unknown> {
  const list = entries(text);
  return list.length > 0 ? { [HARDWARE_PARAM[plugin]]: list } : {};
}

/** hardwareErrors returns one message keyed by the parameter's name when
 * the list would be refused; an empty object means it may be sent. */
export function hardwareErrors(plugin: HardwarePlugin, text: string, t: Messages): Record<string, string> {
  const msg = t.checks.hardware.errors;
  const list = entries(text);
  let error: string | undefined;
  if (list.length > MAX_ENTRIES) error = msg.tooMany(MAX_ENTRIES);
  else if (list.some((e) => byteLen(e) > MAX_BYTES)) error = msg.tooLong(MAX_BYTES);
  else if (list.some((e) => CONTROL.test(e))) error = msg.control;
  else if (plugin === 'smart' && list.some((e) => !DEVICE.test(e) || e.split('/').includes('..'))) error = msg.device;
  return error ? { [HARDWARE_PARAM[plugin]]: error } : {};
}
