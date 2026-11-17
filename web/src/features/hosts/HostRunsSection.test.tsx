import { describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { renderAt } from '../../test/hostDetail.tsx';
import { HostRunsSection } from './HostRunsSection.tsx';

function job(id: string, status: string, kind = 'run') {
  return { id, kind, status, trigger: 'manual', run_group_id: id, zone_id: 'z1', created_at: '2026-11-03T10:00:00Z' };
}

describe('HostRunsSection', () => {
  it('runs section lists runs for the host and links to the filtered list', async () => {
    const user = userEvent.setup();
    let asked: Record<string, string> = {};
    mockApi({
      'GET /api/v1/orgs/acme/jobs': (req: MockRequest) => {
        asked = Object.fromEntries(req.search.entries());
        return { jobs: [job('aaaaaaaa-1111', 'succeeded'), job('bbbbbbbb-2222', 'failed', 'adhoc')], next_cursor: 'more' };
      },
    });
    renderAt(() => <HostRunsSection org="acme" hostId="h1" />);

    expect(await screen.findByRole('link', { name: 'aaaaaaaa' })).toHaveAttribute('href', '/o/acme/runs/aaaaaaaa-1111');
    expect(screen.getByRole('link', { name: 'bbbbbbbb' })).toBeInTheDocument();
    expect(screen.getByText('succeeded')).toBeInTheDocument();
    expect(screen.getByText('failed')).toBeInTheDocument();
    await waitFor(() => expect(asked).toMatchObject({ host_id: 'h1', limit: '5' }));

    const all = screen.getByRole('link', { name: 'All runs for this host' });
    expect(all).toHaveAttribute('href', '/o/acme/runs?host=h1');
    await user.click(all);
    expect(await screen.findByText('RUNS PAGE host=h1')).toBeInTheDocument();
  });

  it('says so when the host has no runs', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/jobs': { jobs: [], next_cursor: '' } });
    renderAt(() => <HostRunsSection org="acme" hostId="h1" />);

    expect(await screen.findByText('No runs have targeted this host yet.')).toBeInTheDocument();
  });

  it('shows a read error', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/jobs': () => new Response(JSON.stringify({ detail: 'jobs store down' }), { status: 500 }) });
    renderAt(() => <HostRunsSection org="acme" hostId="h1" />);

    expect(await screen.findByRole('alert')).toHaveTextContent('jobs store down');
  });

  it('is a teaser on the runs list columns: no pager, no sort buttons, a column can be hidden', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/auth/me/preferences': { density: 'standard', page_size: 25, lists: {} },
      'GET /api/v1/orgs/acme/jobs': { jobs: [job('aaaaaaaa-1111', 'succeeded'), job('bbbbbbbb-2222', 'failed', 'adhoc')], next_cursor: '' },
    });
    renderAt(() => <HostRunsSection org="acme" hostId="h1" />);

    await screen.findByRole('link', { name: 'aaaaaaaa' });
    for (const name of ['Run', 'Kind', 'Status', 'Trigger', 'Template', 'Zone', 'Created']) {
      expect(screen.getByRole('columnheader', { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: 'Kind' })).not.toBeInTheDocument();
    expect(document.querySelector('[data-list-row] [title]')?.getAttribute('title')).toMatch(/2026/);
    expect(screen.queryByRole('button', { name: 'Next page' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Rows per page')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Actions for/ })).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /^Columns/ }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Columns' })).getByRole('checkbox', { name: 'Zone' }));
    expect(screen.queryByRole('columnheader', { name: 'Zone' })).not.toBeInTheDocument();
  });
});
