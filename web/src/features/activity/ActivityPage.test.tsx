import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import type { Locale } from '../../i18n/types.ts';
import { ActivityPage } from './ActivityPage.tsx';
import { validateActivitySearch } from './activitySearch.ts';

const MEMBERS = { members: [{ user_id: '00000000-0000-4000-8000-000000000001', display_name: 'Tim R.', email: 'tim@acme.io', role: 'owner', created_at: '2026-01-01T00:00:00Z', disabled_at: null }] };
const PREFS = { density: 'standard', page_size: 25, lists: {} };

const event = (over: Record<string, unknown> = {}) => ({
  id: 'e1',
  at: '2026-03-01T10:00:00Z',
  actor_kind: 'system',
  actor_id: null,
  action: 'reconciled',
  object_type: 'zone',
  summary: 'reconciled default zone',
  ...over,
});

function many(n: number) {
  return Array.from({ length: n }, (_, i) => event({ id: `e${i + 1}`, summary: `event ${i + 1}` }));
}

/** The answer for one page of `all`, as the server pages it. */
function pageOf(all: ReturnType<typeof many>, req: MockRequest) {
  const page = Number(req.search.get('page') ?? '1');
  const perPage = Number(req.search.get('per_page') ?? '25');
  return { items: all.slice((page - 1) * perPage, page * perPage), next_cursor: '', total: all.length };
}

let requests: URLSearchParams[] = [];

function setup(items: unknown[] | ((req: MockRequest) => unknown), initial = '/o/acme/activity', locale?: Locale) {
  requests = [];
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'GET /api/v1/orgs/acme/members': MEMBERS,
    'GET /api/v1/orgs/acme/activity': (req: MockRequest) => {
      requests.push(req.search);
      return typeof items === 'function' ? items(req) : { items, next_cursor: '', total: items.length };
    },
  });
  return renderListPage({ path: '/activity', component: ActivityPage, initial, locale, extraSearch: (q) => ({ ...validateActivitySearch(q) }) });
}

describe('ActivityPage actor names', () => {
  it('resolves a user actor to their name from the members list', async () => {
    setup([event({ actor_kind: 'user', actor_id: '00000000-0000-4000-8000-000000000001', summary: 'created host web-04', object_type: 'host' })]);
    const row = (await screen.findByText('created host web-04')).closest('[role="row"]') as HTMLElement;
    await waitFor(() => expect(within(row).getByText('Tim R.')).toBeInTheDocument());
    expect(screen.queryByText(/actor_kind/)).not.toBeInTheDocument();
  });

  it('labels a token actor and a system actor without a raw uuid prefix', async () => {
    setup([
      event({ id: 'e1', actor_kind: 'token', actor_id: 'tok1', summary: 'created host web-05' }),
      event({ id: 'e2', summary: 'reconciled default zone' }),
    ]);
    expect(await screen.findByText('API token')).toBeInTheDocument();
    expect(screen.getByText('Pulsix')).toBeInTheDocument();
  });

  it('names the token a change was made with', async () => {
    setup([
      event({ id: 'e1', actor_kind: 'token', actor_id: '00000000-0000-4000-8000-000000000001', token_name: 'helper', summary: 'created host web-06' }),
      event({ id: 'e2', actor_kind: 'user', actor_id: '00000000-0000-4000-8000-000000000001', summary: 'created host web-07' }),
    ]);
    expect(await screen.findByText('Tim R. · via token helper')).toBeInTheDocument();
    expect(screen.queryAllByText(/via token/)).toHaveLength(1);
  });

  it('filters by actor using the member picker, matching the API\'s user-id filter', async () => {
    setup([]);
    await screen.findByText('No activity recorded yet.');
    await pickOption(screen, 'Filter by actor', 'Tim R.');
    await waitFor(() => expect(requests.at(-1)?.get('actor')).toBe('00000000-0000-4000-8000-000000000001'));
  });
});

describe('ActivityPage paging', () => {
  it('asks for page 1 at the saved size, shows the footer and no "Load more"', async () => {
    setup((req) => pageOf(many(30), req));
    expect(await screen.findByText('event 1')).toBeInTheDocument();
    expect(requests).toHaveLength(1);
    expect(requests[0].get('page')).toBe('1');
    expect(requests[0].get('per_page')).toBe('25');
    expect(requests[0].get('sort')).toBeNull();
    expect(requests[0].get('cursor')).toBeNull();
    expect(await screen.findByText('1–25 of 30 events')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Load more' })).not.toBeInTheDocument();
  });

  it('sorts by time through the URL, and only that column sorts', async () => {
    const user = userEvent.setup();
    const router = setup((req) => pageOf(many(30), req));
    await screen.findByText('event 1');
    expect(screen.queryByRole('button', { name: 'Who' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Object' })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /When/ }));
    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('at'));
    expect(requests.at(-1)?.get('order')).toBe('asc');
    expect(router.state.location.search).toMatchObject({ sort: 'at', order: 'asc' });
  });

  it('reads the sort and page from the URL', async () => {
    setup((req) => pageOf(many(30), req), '/o/acme/activity?sort=at&order=desc&page=2');
    expect(await screen.findByText('event 26')).toBeInTheDocument();
    expect(requests[0].get('page')).toBe('2');
    expect(requests[0].get('sort')).toBe('at');
    expect(requests[0].get('order')).toBe('desc');
  });

  it('shows the last page for a page past the end, not the empty state', async () => {
    setup((req) => pageOf(many(30), req), '/o/acme/activity?page=9');
    expect(await screen.findByText('26–30 of 30 events')).toBeInTheDocument();
    expect(screen.queryByText('No activity recorded yet.')).not.toBeInTheDocument();
  });

  it('starts over at page 1 when the object-type filter changes', async () => {
    const user = userEvent.setup();
    const router = setup((req) => pageOf(many(30), req), '/o/acme/activity?page=2');
    await screen.findByText('26–30 of 30 events');
    await user.click(screen.getByRole('button', { name: 'host' }));
    await waitFor(() => expect(requests.at(-1)?.get('object_type')).toBe('host'));
    expect(requests.filter((r) => r.get('object_type') === 'host').every((r) => r.get('page') === '1')).toBe(true);
    expect(router.state.location.search).not.toHaveProperty('page');
  });

  it('starts over at page 1 when the actor filter changes', async () => {
    setup((req) => pageOf(many(30), req), '/o/acme/activity?page=2');
    await screen.findByText('26–30 of 30 events');
    await pickOption(screen, 'Filter by actor', 'Tim R.');
    await waitFor(() => expect(requests.at(-1)?.get('actor')).toBe('00000000-0000-4000-8000-000000000001'));
    expect(requests.filter((r) => r.get('actor') === '00000000-0000-4000-8000-000000000001').every((r) => r.get('page') === '1')).toBe(true);
  });

  it('offers the time, actor, action and object as columns', async () => {
    setup([event()]);
    for (const name of ['When', 'Who', 'What happened', 'Object']) {
      expect(await screen.findByRole('columnheader', { name: new RegExp(name) })).toBeInTheDocument();
    }
  });

  it('says so in German', async () => {
    setup((req) => pageOf(many(30), req), '/o/acme/activity', 'de');
    expect(await screen.findByText('1–25 von 30 Ereignisse')).toBeInTheDocument();
  });
});

describe('ActivityPage automation object types', () => {
  it('filters by the automation object types', async () => {
    const user = userEvent.setup();
    setup([event({ actor_kind: 'schedule', actor_id: 's1', object_type: 'job_template', summary: 'schedule "Nightly" triggered' })]);
    expect(await screen.findByText('schedule "Nightly" triggered')).toBeInTheDocument();
    for (const t of ['schedule', 'trigger', 'worker', 'channel', 'maintenance', 'job_template']) {
      expect(screen.getByRole('button', { name: t })).toBeInTheDocument();
    }
    await user.click(screen.getByRole('button', { name: 'maintenance' }));
    await waitFor(() => expect(requests.map((r) => r.get('object_type'))).toContain('maintenance'));
  });
});

describe('ActivityPage filters in the URL', () => {
  it('reads the actor, the type and the range from the URL and asks the server for them', async () => {
    setup([], '/o/acme/activity?actor=00000000-0000-4000-8000-000000000001&type=host&since=7d');
    await screen.findByText('No activity matches the filters.');
    const last = requests.at(-1)!;
    expect(last.get('actor')).toBe('00000000-0000-4000-8000-000000000001');
    expect(last.get('object_type')).toBe('host');
    expect(Date.now() - new Date(last.get('since')!).getTime()).toBeGreaterThan(6.9 * 86_400_000);
    expect(screen.getByRole('button', { name: 'host', pressed: true })).toBeInTheDocument();
  });

  it('writes the filters to the URL and Reset clears them', async () => {
    const user = userEvent.setup();
    const router = setup((req) => pageOf(many(30), req), '/o/acme/activity?page=2');
    await screen.findByText('26–30 of 30 events');
    await user.click(screen.getByRole('button', { name: 'zone' }));
    await waitFor(() => expect(router.state.location.search).toEqual({ type: 'zone' }));
    await pickOption(screen, 'Filter by actor', 'Tim R.');
    await pickOption(screen, 'Filter by time', 'Last 24 hours');
    await waitFor(() => expect(router.state.location.search).toEqual({ type: 'zone', actor: '00000000-0000-4000-8000-000000000001', since: '1d' }));
    await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(screen.getByRole('button', { name: 'all', pressed: true })).toBeInTheDocument();
  });
});
