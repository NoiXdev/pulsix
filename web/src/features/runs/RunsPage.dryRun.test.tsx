import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { pickOption } from '../../test/pickOption.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunsPage } from './RunsPage.tsx';

const PREFS = { density: 'standard', page_size: 25, lists: {} };
const ORG = { org: { id: 'o', name: 'Acme', slug: 'acme', role: 'operator', created_at: '' } };
const job = (id: string, over: Record<string, unknown> = {}) => ({
  id, zone_id: '0197c0b1-0000-7000-8000-000000000002', kind: 'run', status: 'succeeded', run_group_id: 'g1', trigger: 'manual',
  check_mode: false, created_at: '2026-09-15T08:00:00Z', ...over,
});

function routes(seen: URLSearchParams[], jobs: unknown[] = []) {
  return {
    'GET /api/v1/auth/me/preferences': PREFS,
    'GET /api/v1/orgs/acme': ORG,
    'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
      seen.push(req.search);
      return { jobs, next_cursor: '', total: jobs.length };
    },
  };
}

const renderList = (path = '/o/acme/runs') => renderRoutes([{ path: '/runs', component: RunsPage }], path);

describe('RunsPage dry runs', () => {
  it('filters to the dry runs through the URL and the request', async () => {
    const seen: URLSearchParams[] = [];
    mockApi(routes(seen));
    const router = renderList();
    await screen.findByLabelText('Filter by mode');
    await pickOption(screen, 'Filter by mode', 'Dry runs');
    await waitFor(() => expect(seen.some((q) => q.get('check_mode') === 'true')).toBe(true));
    expect(router.state.location.search).toMatchObject({ check_mode: true });

    await pickOption(screen, 'Filter by mode', 'Real runs');
    await waitFor(() => expect(seen.some((q) => q.get('check_mode') === 'false')).toBe(true));
    expect(router.state.location.search).toMatchObject({ check_mode: false });
  });

  it('reads the dry runs filter from a link', async () => {
    const seen: URLSearchParams[] = [];
    mockApi(routes(seen));
    renderList('/o/acme/runs?check_mode=true');
    await waitFor(() => expect(seen.length).toBeGreaterThan(0));
    expect(seen.every((q) => q.get('check_mode') === 'true')).toBe(true);
    expect(screen.getByLabelText('Filter by mode')).toHaveValue('Dry runs');
  });

  it('marks the dry runs with a badge', async () => {
    mockApi(routes([], [job('0197c0a2-0000-7000-8000-000000000001', { check_mode: true }), job('0197c0a2-0000-7000-8000-000000000002')]));
    renderList();
    const table = await screen.findByRole('table', { name: 'Runs' });
    await waitFor(() => expect(within(table).getAllByRole('row').filter((r) => within(r).queryAllByRole('cell').length > 0)).toHaveLength(2));
    const rows = within(table).getAllByRole('row').filter((r) => within(r).queryAllByRole('cell').length > 0);
    expect(within(rows[0]).getByText('Dry run')).toBeInTheDocument();
    expect(within(rows[1]).queryByText('Dry run')).not.toBeInTheDocument();
  });
});
