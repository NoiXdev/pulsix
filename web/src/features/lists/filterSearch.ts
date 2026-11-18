import { useCallback } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';

/** A free-text filter (a search, an id). `check` narrows what is accepted. */
export interface StringParam {
  kind: 'string';
  check?: (value: string) => boolean;
}
/** One value out of a fixed set. */
export interface EnumParam<V extends string = string> {
  kind: 'enum';
  values: readonly V[];
}
/** Any number of values out of a fixed set: `?status=online,offline`. */
export interface MultiParam<V extends string = string> {
  kind: 'multi';
  values: readonly V[];
}
/** On or absent: `?managed=true`. */
export interface FlagParam {
  kind: 'flag';
}
/** Yes, no or absent (any): `?muted=false`. */
export interface BoolParam {
  kind: 'bool';
}

export type FilterParam = StringParam | EnumParam | MultiParam | FlagParam | BoolParam;

/** A list's filter params by URL key. */
export type FilterSchema = Record<string, FilterParam>;

/** The builders of a schema: `{ q: param.string(), state: param.enum(['open', 'closed']) }`. */
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const param = {
  /** An id the server takes: a UUID-shaped string, so a hand-typed value is no filter (never a 400). */
  uuid: (): StringParam => ({ kind: 'string', check: (v) => UUID.test(v) }),
  string: (check?: (value: string) => boolean): StringParam => ({ kind: 'string', check }),
  enum: <const V extends string>(values: readonly V[]): EnumParam<V> => ({ kind: 'enum', values }),
  multi: <const V extends string>(values: readonly V[]): MultiParam<V> => ({ kind: 'multi', values }),
  flag: (): FlagParam => ({ kind: 'flag' }),
  bool: (): BoolParam => ({ kind: 'bool' }),
};

type UrlValue<P> = P extends StringParam
  ? string
  : P extends EnumParam<infer V>
    ? V
    : P extends MultiParam
      ? string
      : P extends FlagParam
        ? true
        : P extends BoolParam
          ? boolean
          : never;

type Value<P> = P extends MultiParam<infer V> ? V[] : UrlValue<P>;

/** The filters as the route's search holds them (a multi param is its
 * comma-joined values, so the URL stays readable). Absent means unset. */
export type FilterSearch<S extends FilterSchema> = { [K in keyof S]?: UrlValue<S[K]> };

/** The filters as a list reads them (a multi param as an array). */
export type FilterValues<S extends FilterSchema> = { [K in keyof S]?: Value<S[K]> };

/** A string the router may have read as JSON (`?q=42` arrives as 42). */
function text(raw: unknown): string | undefined {
  if (typeof raw === 'string') return raw;
  if (typeof raw === 'number' && Number.isFinite(raw)) return String(raw);
  if (typeof raw === 'boolean') return String(raw);
  return undefined;
}

function decode(p: FilterParam, raw: unknown): string | boolean | undefined {
  switch (p.kind) {
    case 'string': {
      const v = text(raw);
      return v !== undefined && v !== '' && (!p.check || p.check(v)) ? v : undefined;
    }
    case 'enum': {
      const v = text(raw);
      return v !== undefined && p.values.includes(v) ? v : undefined;
    }
    case 'multi': {
      const parts = Array.isArray(raw) ? raw.map(text) : (text(raw)?.split(',') ?? []);
      // The schema's order, each value once: one URL per choice.
      const picked = p.values.filter((v) => parts.includes(v));
      return picked.length ? picked.join(',') : undefined;
    }
    case 'flag':
      return raw === true || raw === 'true' ? true : undefined;
    case 'bool':
      return raw === true || raw === 'true' ? true : raw === false || raw === 'false' ? false : undefined;
  }
}

/**
 * Reads a list's filters out of a route's raw search. A list route's
 * `validateSearch` spreads it next to validateListSearch:
 * `(s) => ({ ...validateListSearch(s), ...validateFilters(SCHEMA, s) })`.
 * An unknown or malformed value is dropped (no filter), so a hand-edited
 * or stale link still opens the list.
 */
export function validateFilters<S extends FilterSchema>(schema: S, search: Record<string, unknown>): FilterSearch<S> {
  const out: Record<string, unknown> = {};
  for (const [key, p] of Object.entries(schema)) out[key] = decode(p, search[key]);
  return out as FilterSearch<S>;
}

/** The URL form of one value: undefined (and '', [], false for a flag)
 * leaves the key out. Invalid values are dropped as validateFilters would. */
function encode(p: FilterParam, value: unknown): string | boolean | undefined {
  if (value === undefined || value === null) return undefined;
  if (p.kind === 'multi') return decode(p, Array.isArray(value) ? value : [value]);
  return decode(p, value);
}

function read<S extends FilterSchema>(schema: S, search: FilterSearch<S>): FilterValues<S> {
  const out: Record<string, unknown> = {};
  for (const [key, p] of Object.entries(schema)) {
    const v = (search as Record<string, unknown>)[key];
    if (v === undefined) continue;
    out[key] = p.kind === 'multi' ? String(v).split(',') : v;
  }
  return out as FilterValues<S>;
}

export interface SetFilterOptions {
  /** Replace the history entry instead of adding one: for typing. */
  replace?: boolean;
}

export interface ListFilters<S extends FilterSchema> {
  /** The filters set now; unset keys are absent. */
  filters: FilterValues<S>;
  /** Any filter is set (for a Reset button, a "no match" empty state). */
  active: boolean;
  /** Sets one filter (undefined clears it) and goes back to page 1. */
  setFilter<K extends keyof S & string>(key: K, value: Value<S[K]> | undefined, opts?: SetFilterOptions): void;
  /** Sets several filters in one navigation (one history entry). */
  setFilters(patch: FilterValues<S>, opts?: SetFilterOptions): void;
  /** Clears every filter of the schema; sort stays, page goes. */
  reset(): void;
}

/**
 * useListFilters reads a list's filters from the URL and writes them back
 * through the router, beside the list's sort and page (listSearch.ts).
 * Every change drops `page` (a new filter starts at page 1) and keeps the
 * sort and any key outside the schema; `replace` keeps a typed search from
 * adding a history entry per keystroke. Back and a reload bring the same
 * filters back because the URL is their only home.
 */
export function useListFilters<S extends FilterSchema>(schema: S): ListFilters<S> {
  const navigate = useNavigate();
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const filters = read(schema, validateFilters(schema, raw));

  const write = useCallback(
    (patch: Record<string, unknown>, opts?: SetFilterOptions) => {
      const encoded: Record<string, unknown> = {};
      for (const [key, value] of Object.entries(patch)) {
        if (Object.hasOwn(schema, key)) encoded[key] = encode(schema[key], value);
      }
      void navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({ ...prev, ...encoded, page: undefined }),
        replace: opts?.replace,
        resetScroll: false,
      });
    },
    [navigate, schema],
  );

  return {
    filters,
    active: Object.keys(filters).length > 0,
    setFilter: (key, value, opts) => write({ [key]: value }, opts),
    setFilters: (patch, opts) => write(patch as Record<string, unknown>, opts),
    reset: () => write(Object.fromEntries(Object.keys(schema).map((k) => [k, undefined]))),
  };
}
