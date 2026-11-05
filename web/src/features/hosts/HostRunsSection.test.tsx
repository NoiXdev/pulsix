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

  it('each recent run is a link row', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/jobs': { jobs: [job('aaaaaaaa-1111', 'succeeded')], next_cursor: '' } });
    renderAt(() => <HostRunsSection org="acme" hostId="h1" />);

    const row = await screen.findByRole('link', { name: 'aaaaaaaa' });
    expect(row).toHaveAttribute('href', '/o/acme/runs/aaaaaaaa-1111');
    expect(row).toHaveTextContent('succeeded');
    expect(row).toHaveTextContent('run · Manual');
    expect(within(row).queryAllByRole('link')).toEqual([]);
    expect(within(row).queryAllByRole('button')).toEqual([]);
    expect(screen.queryByRole('button', { name: /^Actions for/ })).not.toBeInTheDocument();
  });
});
