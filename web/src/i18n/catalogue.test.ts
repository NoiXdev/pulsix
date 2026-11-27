import { describe, expect, it } from 'vitest';
import { en } from './en/index.ts';
import { de } from './de/index.ts';

/**
 * The type system already refuses a German catalogue with a missing or an
 * extra key (de/*.ts is typed as Messages). What it cannot see is a key
 * that is present but empty — '' satisfies `string` — which would render
 * as a blank button on a German page. This walks every leaf at run time.
 */
type Leaf = { path: string; value: unknown };

function leaves(node: unknown, path = ''): Leaf[] {
  if (node !== null && typeof node === 'object') {
    return Object.entries(node).flatMap(([k, v]) => leaves(v, path ? `${path}.${k}` : k));
  }
  return [{ path, value: node }];
}

/** A message function is called with one placeholder argument per
 * parameter; every parameter in the catalogue is a string or a number,
 * and "1" reads as either. */
function render(value: unknown): unknown {
  if (typeof value !== 'function') return value;
  return (value as (...args: unknown[]) => unknown)(...Array.from({ length: value.length }, () => 1));
}

describe('the German catalogue', () => {
  it('has no empty leaf', () => {
    const empty = leaves(de)
      .map(({ path, value }) => ({ path, out: render(value) }))
      .filter(({ out }) => typeof out !== 'string' || out.trim() === '')
      .map(({ path }) => path);
    expect(empty).toEqual([]);
  });

  it('has exactly the keys of the English one, each of the same kind', () => {
    const shape = (m: unknown) => leaves(m).map(({ path, value }) => `${path}:${typeof value}`).sort();
    expect(shape(de)).toEqual(shape(en));
  });
});

describe('the failure reasons of the synthetic checks', () => {
  // The <plugin>.failure vocabulary of internal/probe (failure.go,
  // FailureLabels), in its order; a label added there needs a text here.
  const VOCABULARY = ['none', 'dns', 'refused', 'timeout', 'unreachable', 'reset', 'tls', 'certificate', 'protocol', 'redirects', 'nxdomain', 'servfail', 'auth', 'auth_or_lost', 'error'];
  it.each([['English', en], ['German', de]])('have a text for every label in the %s catalogue', (_name, catalogue) => {
    expect(Object.keys(catalogue.checks.synthetic.failures)).toEqual(VOCABULARY);
  });
});

describe('the save-failure messages of the settings pages', () => {
  // Shown as a toast on their own, so they start like a sentence.
  const PATHS = ['settings.general.saveFailed', 'language.account.saveFailed', 'language.instance.saveFailed'];
  it.each([['English', en], ['German', de]])('start with a capital letter in the %s catalogue', (_name, catalogue) => {
    const all = new Map(leaves(catalogue).map(({ path, value }) => [path, value]));
    for (const path of PATHS) expect(`${path}: ${String(all.get(path))}`).toMatch(/: [A-ZÄÖÜ]/);
  });
});

describe('count messages for a count of one', () => {
  // Backlog 1.42: these read "1 members", "1 samples", "1 host(s)" before.
  it.each([
    [en.checks.list.summary(1), '1 definition ·'],
    [en.checks.groups.summary(1), '1 group ·'],
    [en.checks.rules.summary(1), '1 rule ·'],
    [en.checks.items.summary(1), '1 decision ·'],
    [en.credentials.list.summary(1), '1 credential ·'],
    [en.credentials.detail.rotationStarted('deploy', 1), 'Rotation of deploy started (1 job)'],
    [en.playbooks.list.summary(1), '1 playbook ·'],
    [en.templates.list.summary(1), '1 template ·'],
    [en.repos.list.summary(1), '1 git repository ·'],
    [en.inventory.groups.summary(1), '1 inventory group ·'],
    [en.inventory.children.members(1), '1 member'],
    [en.inventory.schemaTab.savedIncomplete(1), 'Schema saved · 1 member needs new values'],
    [en.inventory.schemaTab.summary(1, 0), '1 field ·'],
    [en.schedules.summary(1, 1), '1 schedule ·'],
    [en.schedules.pausedCount(1), '1 schedule paused'],
    [en.runs.adhoc.started(1), 'Command started on 1 host'],
    [en.runs.adhoc.selected(1), '1 host selected'],
    [en.hosts.alertRoutes.escalating(1), '1 route below escalates independently'],
    [en.hosts.checksTab.value.daysLeft('1'), '1 day left'],
    [en.playbooks.page.dryRun.latest.zones(1), '1 zone'],
    [en.ui.chart.label('cpu', 'over the last hour', 1), 'cpu over the last hour, 1 sample'],
    [en.appearance.columns.state(1, 1), '1 of 1 column'],
    [en.shell.nav.runRequestsBadge(1), '1 pending run request'],
    [de.hosts.checksTab.value.daysLeft('1'), 'noch 1 Tag'],
    [de.playbooks.page.dryRun.latest.zones(1), '1 Zone'],
    [de.ui.chart.label('cpu', 'in der letzten Stunde', 1), 'cpu, in der letzten Stunde, 1 Messwert'],
    [de.appearance.columns.state(1, 1), '1 von 1 Spalte'],
    [de.shell.nav.runRequestsBadge(1), '1 offene Laufanfrage'],
  ])('%s', (out, expected) => {
    // A prefix of the message, not followed by a letter: "1 member" must
    // not pass on "1 members".
    expect(out.startsWith(expected)).toBe(true);
    expect(out.slice(expected.length)).not.toMatch(/^\p{L}/u);
  });
});
