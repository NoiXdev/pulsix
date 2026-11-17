import { describe, expect, it } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderListPage } from '../../test/renderListPage.tsx';
import { pickOption } from '../../test/pickOption.ts';
import { firstCells, hideColumn, numbered, sortBy } from '../../test/wholeList.ts';
import { RunRequestsPage } from './RunRequestsPage.tsx';
import { request } from './runRequestTestKit.ts';

const PREFS = { density: 'standard', page_size: 25, lists: {} };

function setup() {
  mockApi({
    'GET /api/v1/auth/me/preferences': PREFS,
    'PUT /api/v1/auth/me/preferences': (req: MockRequest) => req.body,
    'GET /api/v1/orgs/acme/job-templates': { templates: [] },
    'GET /api/v1/orgs/acme/run-requests': {
      requests: numbered('ask', 30).map((reason) => request({ id: `id-${reason}`, reason, status: 'approved' })),
    },
  });
  return renderListPage({
    path: '/runs/requests',
    component: RunRequestsPage,
    initial: '/o/acme/runs/requests',
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
});
