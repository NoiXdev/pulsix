import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, within } from '@testing-library/react';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs } from './runRequestTestKit.ts';

/** A dry run narrowed to web-01 and a host that left the template since,
 * with a limit and an extra var of its own. */
const OVERRIDES = {
  check_mode: true,
  diff: true,
  limit_override: 'web-0*',
  extra_vars_override: { nginx_workers: 8, release: { tag: 'v2' } },
  hosts: ['h1', 'h-gone'],
  targets: [{ id: 'h1', name: 'web-01', address: '10.0.0.11', ssh_user: 'deploy', ssh_port: 22 }],
};

function renderDetail(picture: Record<string, unknown>, lang?: 'de') {
  mockApi({
    'GET /api/v1/orgs/acme': orgAs('operator'),
    'GET /api/v1/orgs/acme/members': MEMBERS,
    [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: detail({ picture }),
  });
  return renderRoutes(
    [{ path: '/runs/requests/$id', component: RunRequestDetailPage }],
    `/o/acme/runs/requests/${RQ}`,
    new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    lang,
  );
}

describe('RunRequestDetailPage overrides', () => {
  it('shows a dry run, its narrowed hosts, limit and extra vars', async () => {
    renderDetail(OVERRIDES);

    const banner = await screen.findByRole('note', { name: 'Dry run (check mode, diff)' });
    expect(within(banner).getByText(/starts no follow-ups/)).toBeInTheDocument();

    const template = screen.getByRole('region', { name: 'Template' });
    expect(within(template).getByText('Limit of this request')).toBeInTheDocument();
    expect(within(template).getByText('web-0*')).toBeInTheDocument();

    const targets = screen.getByRole('region', { name: 'Targets' });
    expect(within(targets).getByText('This request narrows the run to 2 hosts.')).toBeInTheDocument();
    expect(within(targets).getByText(/no longer among the template's targets/)).toHaveTextContent('h-gone');
    expect(within(targets).getAllByRole('row')).toHaveLength(2);

    const vars = screen.getByRole('region', { name: 'Extra vars' });
    const own = within(vars).getByRole('table', { name: 'Set by this request' });
    expect(within(own).getByText('8')).toBeInTheDocument();
    expect(within(own).getByText('{"tag":"v2"}')).toBeInTheDocument();
    // The template's own value of a name the request sets is marked replaced.
    expect(within(vars).getByText('replaced by this request')).toBeInTheDocument();
  });

  it('says when a dry run shows no diff', async () => {
    renderDetail({ ...OVERRIDES, diff: false });
    expect(await screen.findByRole('note', { name: 'Dry run (check mode)' })).toBeInTheDocument();
  });

  it('shows nothing of it for a request without overrides', async () => {
    renderDetail({});
    expect(await screen.findByRole('region', { name: 'Template' })).toBeInTheDocument();
    expect(screen.queryByRole('note', { name: /Dry run/ })).not.toBeInTheDocument();
    expect(screen.queryByText('Limit of this request')).not.toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Set by this request' })).not.toBeInTheDocument();
    expect(screen.queryByText(/narrows the run/)).not.toBeInTheDocument();
  });

  it('speaks German', async () => {
    renderDetail(OVERRIDES, 'de');
    const banner = await screen.findByRole('note', { name: 'Probelauf (Check-Modus, Diff)' });
    expect(within(banner).getByText(/startet keine Folgeläufe/)).toBeInTheDocument();
    expect(screen.getByText('Limit dieser Anfrage')).toBeInTheDocument();
    expect(screen.getByText('Diese Anfrage beschränkt den Lauf auf 2 Hosts.')).toBeInTheDocument();
    expect(screen.getByRole('table', { name: 'Von dieser Anfrage gesetzt' })).toBeInTheDocument();
    expect(screen.getByText('durch diese Anfrage ersetzt')).toBeInTheDocument();
  });
});
