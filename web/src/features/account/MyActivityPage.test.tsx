import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { MyActivityPage } from './MyActivityPage.tsx';

const ORGS = { orgs: [{ id: 'o1', slug: 'acme', name: 'Acme', role: 'owner', created_at: '2026-01-01T00:00:00Z' }] };

function events(n: number, from = 1) {
  return Array.from({ length: n }, (_, i) => ({
    id: `e${from + i}`,
    at: '2026-11-17T10:00:00Z',
    action: 'host.create',
    actor_kind: 'user',
    actor_id: 'u1',
    object_id: `h${from + i}`,
    object_type: 'host',
    org_id: 'o1',
    summary: `Created host ${from + i}`,
  }));
}

let requests: URLSearchParams[] = [];

function setup(opts: { prefs?: object; items?: unknown[]; total?: number } = {}) {
  requests = [];
  mockApi({
    'GET /api/v1/orgs': ORGS,
    'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {}, ...opts.prefs },
    'GET /api/v1/me/activity': (req: MockRequest) => {
      requests.push(req.search);
      return { items: opts.items ?? events(25), next_cursor: '', total: opts.total ?? 40 };
    },
  });
  return renderRoutes([{ path: '/activity', component: MyActivityPage }], '/o/acme/activity');
}

describe('MyActivityPage', () => {
  it('asks for page 1 at the saved size and shows the footer', async () => {
    setup();

    expect(await screen.findByText('Created host 1')).toBeInTheDocument();
    expect(requests).toHaveLength(1);
    expect(requests[0].get('page')).toBe('1');
    expect(requests[0].get('per_page')).toBe('25');
    expect(await screen.findByText('1–25 of 40 events')).toBeInTheDocument();
  });

  it('shows the organization name, the summary and the object type per event', async () => {
    setup({ items: events(1), total: 1 });

    const row = (await screen.findByText('Created host 1')).closest('[role="row"]') as HTMLElement;
    expect(within(row).getByText('Acme')).toBeInTheDocument();
    expect(within(row).getByText('host')).toBeInTheDocument();
  });

  it('waits for the preferences, so the first request has the saved page size', async () => {
    setup({ prefs: { page_size: 50 } });

    await screen.findByText('Created host 1');
    expect(requests.map((r) => r.get('per_page'))).toEqual(['50']);
  });

  it('sorts on the server when the When header is pressed', async () => {
    const user = userEvent.setup();
    const router = setup();

    await screen.findByText('Created host 1');
    await user.click(screen.getByRole('button', { name: /When/ }));

    await waitFor(() => expect(requests.at(-1)?.get('sort')).toBe('at'));
    expect(requests.at(-1)?.get('order')).toBe('asc');
    expect(router.state.location.search).toMatchObject({ sort: 'at', order: 'asc' });
  });

  it('filters by object type and goes back to page 1', async () => {
    const user = userEvent.setup();
    setup();

    await screen.findByText('Created host 1');
    await user.click(screen.getByRole('button', { name: 'token' }));

    await waitFor(() => expect(requests.at(-1)?.get('object_type')).toBe('token'));
    expect(requests.at(-1)?.get('page')).toBe('1');
  });

  it('says so when there is no activity', async () => {
    setup({ items: [], total: 0 });
    expect(await screen.findByText('You have not done anything yet.')).toBeInTheDocument();
  });
});
