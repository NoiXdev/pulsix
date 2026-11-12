import { describe, expect, it } from 'vitest';
import { de } from '../../i18n/de/index.ts';
import { en } from '../../i18n/en/index.ts';
import type { Messages } from '../../i18n/types.ts';
import { CHECK_PLUGINS } from '../../test/checkPlugins.ts';
import { paramText } from './paramForm.ts';

/**
 * Every plugin and parameter GET /check-plugins describes needs its texts
 * in both catalogues (backlog 3.54): the forms render whatever the
 * descriptors list, so a plugin or a parameter added on the manager shows
 * up in the form — with its bare name as the label, unless this fails
 * first. checkPlugins.json is the route's body as the manager serves it
 * (internal/checkplugins regenerates it), so this follows the descriptors.
 */
describe.each([
  ['English', en],
  ['German', de],
] as [string, Messages][])('the %s catalogue', (_name, t) => {
  it('labels every parameter of every plugin', () => {
    const missing = CHECK_PLUGINS.flatMap((p) =>
      p.params.filter((s) => !paramText(t, p.name, s.name)?.label).map((s) => `${p.name}.${s.name}`),
    );
    expect(missing).toEqual([]);
  });

  it('names every plugin in both plugin pickers', () => {
    const dialog: Readonly<Record<string, string>> = t.checks.dialog.plugins;
    const shared: Readonly<Record<string, string>> = t.instance.shared.checks.plugins;
    const missing = CHECK_PLUGINS.filter((p) => !dialog[p.name] || !shared[p.name]).map((p) => p.name);
    expect(missing).toEqual([]);
  });

  it('labels only options an enum offers', () => {
    const stray = CHECK_PLUGINS.flatMap((p) =>
      p.params.flatMap((s) =>
        Object.keys(paramText(t, p.name, s.name)?.options ?? {})
          .filter((o) => !(s.options ?? []).includes(o))
          .map((o) => `${p.name}.${s.name}.${o}`),
      ),
    );
    expect(stray).toEqual([]);
  });

  it('keeps no texts for a parameter the descriptors do not list', () => {
    const params: Readonly<Record<string, Readonly<Record<string, unknown>>>> = t.checks.params;
    const stale = Object.entries(params).flatMap(([plugin, byParam]) =>
      Object.keys(byParam)
        .filter((param) => !CHECK_PLUGINS.some((p) => p.name === plugin && p.params.some((s) => s.name === param)))
        .map((param) => `${plugin}.${param}`),
    );
    expect(stale).toEqual([]);
  });

  it('has a message for every format a descriptor uses', () => {
    const formats: Readonly<Record<string, string>> = t.checks.paramErrors.formats;
    const used = CHECK_PLUGINS.flatMap((p) => p.params.flatMap((s) => (s.format ? [s.format] : [])));
    expect(used.filter((f) => !formats[f])).toEqual([]);
  });
});
