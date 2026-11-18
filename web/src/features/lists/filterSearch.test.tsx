import { describe, expect, it } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { createMemoryHistory, createRootRoute, createRoute, createRouter, RouterProvider } from '@tanstack/react-router';
import { param, useListFilters, validateFilters, type ListFilters } from './filterSearch.ts';
import { validateListSearch } from './listSearch.ts';

const SCHEMA = {
  q: param.string(),
  zone: param.string((v) => /^z\d$/.test(v)),
  state: param.enum(['open', 'closed']),
  status: param.multi(['online', 'offline', 'outdated']),
  managed: param.flag(),
  muted: param.bool(),
};

describe('validateFilters', () => {
  it('keeps valid values', () => {
    expect(
      validateFilters(SCHEMA, { q: 'web', zone: 'z1', state: 'open', status: 'offline,online', managed: true, muted: false }),
    ).toEqual({ q: 'web', zone: 'z1', state: 'open', status: 'online,offline', managed: true, muted: false });
  });

  it('drops unknown and invalid values', () => {
    const out = validateFilters(SCHEMA, { q: '', zone: 'eu', state: 'bogus', status: 'gone', managed: 'yes', muted: 'maybe', other: 'x' });
    expect(Object.values(out).filter((v) => v !== undefined)).toEqual([]);
    expect(out).not.toHaveProperty('other');
  });

  it('reads strings the router parsed as JSON', () => {
    expect(validateFilters(SCHEMA, { q: 42, managed: 'true', muted: 'false' })).toMatchObject({ q: '42', managed: true, muted: false });
  });

  it('keeps the known values of a multi param, in the schema order, once', () => {
    expect(validateFilters(SCHEMA, { status: 'outdated,bogus,online,online' }).status).toBe('online,outdated');
    expect(validateFilters(SCHEMA, { status: ['offline', 'online'] }).status).toBe('online,offline');
  });

  it('composes with the list search', () => {
    expect({ ...validateListSearch({ page: '3', sort: 'name' }), ...validateFilters(SCHEMA, { state: 'closed' }) }).toMatchObject({
      page: 3,
      sort: 'name',
      state: 'closed',
    });
  });
});

function mount(initial: string) {
  let api: ListFilters<typeof SCHEMA> | undefined;
  function Page() {
    api = useListFilters(SCHEMA);
    return <div data-testid="filters">{JSON.stringify(api.filters)}</div>;
  }
  const rootRoute = createRootRoute();
  const listRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/list',
    validateSearch: (s: Record<string, unknown>) => ({ ...validateListSearch(s), ...validateFilters(SCHEMA, s) }),
    component: Page,
  });
  const router = createRouter({
    routeTree: rootRoute.addChildren([listRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
  });
  render(<RouterProvider router={router} />);
  return { router, api: () => api! };
}

const shown = () => JSON.parse(screen.getByTestId('filters').textContent ?? '{}');

describe('useListFilters', () => {
  it('reads the filters from the URL, a multi param as an array', async () => {
    mount('/list?status=online,offline&managed=true&state=nope');
    await screen.findByTestId('filters');
    expect(shown()).toEqual({ status: ['online', 'offline'], managed: true });
  });

  it('writes a filter, keeps the sort and drops the page', async () => {
    const { router, api } = mount('/list?sort=name&order=desc&page=3');
    await screen.findByTestId('filters');
    await act(async () => api().setFilter('state', 'open'));
    expect(router.state.location.search).toEqual({ sort: 'name', order: 'desc', state: 'open' });
    expect(shown()).toEqual({ state: 'open' });
    expect(api().active).toBe(true);
  });

  it('writes a multi param comma-joined and clears it with an empty list', async () => {
    const { router, api } = mount('/list');
    await screen.findByTestId('filters');
    await act(async () => api().setFilter('status', ['outdated', 'online']));
    expect(router.state.location.search).toEqual({ status: 'online,outdated' });
    expect(router.state.location.href).toContain('status=online%2Coutdated');
    await act(async () => api().setFilter('status', []));
    expect(router.state.location.search).toEqual({});
  });

  it('adds a history entry per change, so Back restores the previous filter', async () => {
    const { router, api } = mount('/list?page=2');
    await screen.findByTestId('filters');
    await act(async () => api().setFilter('managed', true));
    expect(router.state.location.search).toEqual({ managed: true });
    await act(async () => router.history.back());
    await screen.findByText('{}');
    expect(router.state.location.search).toEqual({ page: 2 });
  });

  it('replaces the history entry for typing', async () => {
    const { router, api } = mount('/list');
    await screen.findByTestId('filters');
    const before = router.history.length;
    await act(async () => api().setFilter('q', 'w', { replace: true }));
    await act(async () => api().setFilter('q', 'we', { replace: true }));
    expect(router.history.length).toBe(before);
    expect(router.state.location.search).toEqual({ q: 'we' });
  });

  it('drops an invalid value on write', async () => {
    const { router, api } = mount('/list?zone=z1');
    await screen.findByTestId('filters');
    await act(async () => api().setFilter('zone', 'eu'));
    expect(router.state.location.search).toEqual({});
  });

  it('sets several filters in one navigation', async () => {
    const { router, api } = mount('/list');
    await screen.findByTestId('filters');
    const before = router.history.length;
    await act(async () => api().setFilters({ state: 'closed', muted: false }));
    expect(router.history.length).toBe(before + 1);
    expect(router.state.location.search).toEqual({ state: 'closed', muted: false });
  });

  it('resets every filter of the schema and keeps the sort and other keys', async () => {
    const { router, api } = mount('/list?sort=name&q=web&managed=true&page=2&tab=x');
    await screen.findByTestId('filters');
    await act(async () => api().reset());
    expect(router.state.location.search).toEqual({ sort: 'name', tab: 'x' });
    expect(api().active).toBe(false);
  });
});
