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
        return detail({
          fingerprint: `fp-${loads}`,
          picture: { follow_ups: { start: true, templates: [{ template_id: 'f1', template_name: 'reload-lb', template_revision: 3 }, { template_id: 'f2', template_name: 'x', stop: 'in_chain' }] } },
        });
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

    const followUps = screen.getByRole('region', { name: 'Folgeläufe' });
    expect(within(followUps).getByRole('checkbox', { name: 'reload-lb' })).toBeChecked();
    expect(within(followUps).getByText(/Revision 3/)).toBeInTheDocument();
    expect(within(followUps).getByText(/schon in dieser Kette/)).toBeInTheDocument();
    expect(within(followUps).getByText(/Folgeläufe starten später, wenn dieser Lauf erfolgreich ist/)).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Freigeben' }));
    expect(
      await screen.findByText('Diese Anfrage hat sich geändert, seit du sie geöffnet hast. Bitte prüfe sie erneut.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ablehnen' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Freigeben' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Neue Fassung ansehen' }));
    expect(screen.getByRole('button', { name: 'Freigeben' })).toBeEnabled();
  });

  it('names the token and its owner and warns about a revoked token in German', async () => {
    mockApi({
      'GET /api/v1/orgs/acme': orgAs('operator'),
      'GET /api/v1/orgs/acme/members': MEMBERS,
      [`GET /api/v1/orgs/acme/run-requests/${RQ}`]: detail({
        request: { token_state: 'revoked' },
        picture: { repo: { id: 'r1', name: 'infra', url: 'u', branch: 'main', subpath: '', git_credential: 'git-deploy' } },
      }),
    });
    renderRoutes(
      [{ path: '/runs/requests/$id', component: RunRequestDetailPage }],
      `/o/acme/runs/requests/${RQ}`,
      new QueryClient({ defaultOptions: { queries: { retry: false } } }),
      'de',
    );
    expect(await screen.findByText('claude-ops')).toBeInTheDocument();
    expect(await screen.findByText('gehört Tim R.')).toBeInTheDocument();
    expect(
      screen.getByText('Das Token, das diesen Lauf angefragt hat, darf hier keine Läufe mehr anfragen. Eine Freigabe startet den Lauf trotzdem.'),
    ).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Playbook und Repository' })).getByText('Git-Zugangsdaten')).toBeInTheDocument();
  });
});
