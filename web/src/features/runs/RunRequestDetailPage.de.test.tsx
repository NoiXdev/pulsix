import { describe, expect, it } from 'vitest';
import { QueryClient } from '@tanstack/react-query';
import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockApi } from '../../test/mockApi.ts';
import { renderRoutes } from '../../test/renderRoutes.tsx';
import { RunRequestDetailPage } from './RunRequestDetailPage.tsx';
import { MEMBERS, RQ, detail, orgAs } from './runRequestTestKit.ts';

describe('RunRequestDetailPage in German', () => {
  it('shows the picture, the decision and the changed notice in German', async () => {
    const user = userEvent.setup();
    let loads = 0;
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: () => {
        loads += 1;
        return detail({ fingerprint: `fp-${loads}` });
      },
      [`POST /api/v1/orgs/acme/run-requests/${RQ}/approve`]: () =>
        new Response(JSON.stringify({ status: 409, detail: 'changed since you looked at it; review it again' }), {
          status: 409,
          headers: { 'Content-Type': 'application/json' },
        }),
    });
    renderRoutes(
      [{ path: '/runs/requests/$id', component: RunRequestDetailPage }],
      `/o/acme/runs/requests/${RQ}`,
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
      'de',
    );

    expect(await screen.findByRole('heading', { name: 'Laufanfrage' })).toBeInTheDocument();
    expect(screen.getAllByText('offen').length).toBeGreaterThan(0);
    expect(screen.getByRole('region', { name: 'Playbook und Repository' })).toBeInTheDocument();
    const targets = screen.getByRole('region', { name: 'Ziele' });
    expect(within(targets).getByText('Sprunghost')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Extra-Variablen' })).getByText('geheim (verborgen)')).toBeInTheDocument();
    expect(screen.getByText('Die Freigabe startet genau das, was diese Seite zeigt.')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Freigeben' }));
    expect(
      await screen.findByText('Diese Anfrage hat sich geändert, seit du sie geöffnet hast. Bitte prüfe sie erneut.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ablehnen' })).toBeInTheDocument();
  });
});
