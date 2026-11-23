import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { RunRequestsPage } from './RunRequestsPage.tsx';
import { request } from './runRequestTestKit.ts';
import { validateRunRequestsSearch } from './runRequestsSearch.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };

function setup(initial = '/o/acme/runs/requests', requests?: unknown[]) {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/job-templates': { templates: [{ id: 'tpl1', name: 'deploy' }, { id: 'tpl2', name: 'backup' }] },
    'GET /api/v1/orgs/acme/run-requests/pending-count': { count: 4 },
    'GET /api/v1/orgs/acme/run-requests': {
      requests: requests ?? numbered('ask', 30).map((reason) => request({ id: `id-${reason}`, reason, status: 'approved' })),
    },
  });
  return renderListPage({
    path: '/runs/requests',
    component: RunRequestsPage,
    initial,
    extraSearch: (s) => ({ ...validateRunRequestsSearch(s) }),
    others: [{ path: '/runs/requests/$id', component: () => <div>REQUEST DETAIL</div> }],
  });
}

describe('RunRequestsPage list', () => {
  it('sorts by reason in the browser, ask9 before ask10', async () => {
    const user = userEvent.setup();
    const router = setup();
    await screen.findByText('1–25 of 30 requests');
    await sortBy(user, 'Reason');
    await waitFor(() => expect(firstCells().slice(7, 10)).toEqual(['ask8', 'ask9', 'ask10']));
    expect(router.state.location.search).toMatchObject({ sort: 'reason', order: 'asc' });
  });

  it('has no row menu (a request opens through its link) and no inline buttons', async () => {
    setup();
    await screen.findByText('1–25 of 30 requests');
    expect(screen.queryAllByRole('button', { name: /^Actions for/ })).toHaveLength(0);
    expect(screen.getByRole('link', { name: 'ask30' })).toHaveAttribute('href', '/o/acme/runs/requests/id-ask30');
  });

  it('gives the reason once per row: the link names the request, the cell adds no repeat of it', async () => {
    setup();
    await screen.findByText('1–25 of 30 requests');
    const link = screen.getByRole('link', { name: 'ask30' });
    expect(link).toHaveAccessibleName('ask30');
    expect(link).toHaveAccessibleDescription('');
    expect(firstCells()[0]).toBe('ask30');
  });

  it('hides a column from the Columns menu', async () => {
    const user = userEvent.setup();
    setup();
    await screen.findByText('1–25 of 30 requests');
    await hideColumn(user, 'Kind');
    expect(screen.queryByRole('columnheader', { name: 'Kind' })).not.toBeInTheDocument();
  });

  it('pages 30 rows and follows the page size', async () => {
    const user = userEvent.setup();
    setup();
    await user.click(await screen.findByRole('button', { name: 'Next page' }));
    expect(await screen.findByText('26–30 of 30 requests')).toBeInTheDocument();
    await pickOption(screen, 'Rows per page', '10');
    expect(await screen.findByText('1–10 of 30 requests')).toBeInTheDocument();
  });
  describe('filters', () => {
    const rows = [
      request({ id: 'id-a', reason: 'alpha', status: 'pending' }),
      request({ id: 'id-b', reason: 'beta', status: 'approved', template_id: 'tpl2' }),
      request({ id: 'id-c', reason: 'gamma', status: 'rejected', kind: 'retry_failed' }),
    ];

    it('reads the filters from the URL and narrows the rows', async () => {
      setup('/o/acme/runs/requests?status=approved', rows);
      expect(await screen.findByText('1–1 of 1 request')).toBeInTheDocument();
      expect(firstCells()).toEqual(['beta']);
      // The server filtered by status, so the unfiltered total is not known here.
      expect(screen.getByText('1 request')).toBeInTheDocument();
      // The header's pending number comes from the count route instead.
      expect(await screen.findByText('4 pending')).toBeInTheDocument();
    });

    it('asks the server for the chosen status instead of filtering every request in the browser', async () => {
      const queries: string[] = [];
      mockApi({
        'GET /api/v1/auth/me/preferences': PREFS,
        'GET /api/v1/orgs/acme/job-templates': { templates: [] },
        'GET /api/v1/orgs/acme/run-requests': (req: MockRequest) => {
          queries.push(req.search.get('status') ?? '');
          return { requests: rows.filter((q) => !req.search.get('status') || q.status === req.search.get('status')) };
        },
      });
      renderListPage({
        path: '/runs/requests',
        component: RunRequestsPage,
        initial: '/o/acme/runs/requests?status=approved',
        extraSearch: (q) => ({ ...validateRunRequestsSearch(q) }),
      });
      expect(await screen.findByText('1–1 of 1 request')).toBeInTheDocument();
      expect(queries).toContain('approved');
      expect(queries).not.toContain('');
    });

    it('writes the template and the kind to the URL', async () => {
      const user = userEvent.setup();
      const router = setup('/o/acme/runs/requests', rows);
      await screen.findByText('1–3 of 3 requests');
      await pickOption(screen, 'Filter by template', 'backup');
      await waitFor(() => expect(router.state.location.search).toEqual({ template: 'tpl2' }));
      expect(firstCells()).toEqual(['beta']);
      await user.click(screen.getByRole('button', { name: 'Reset every filter' }));
      await pickOption(screen, 'Filter by kind', { value: 'retry_failed' });
      await waitFor(() => expect(router.state.location.search).toEqual({ kind: 'retry_failed' }));
      expect(firstCells()).toEqual(['gamma']);
    });

    it('says when no request matches', async () => {
      setup('/o/acme/runs/requests?status=expired', rows);
      expect(await screen.findByText('No run requests match the filters.')).toBeInTheDocument();
    });
  });
});
