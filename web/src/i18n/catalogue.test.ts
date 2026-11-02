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
