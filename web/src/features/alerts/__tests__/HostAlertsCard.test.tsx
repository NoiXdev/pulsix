import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { HostAlertsCard } from '../HostAlertsCard.tsx';

const alert = (over: Record<string, unknown> = {}) => ({
  id: 'a1',
  host_id: 'h1',
  host_name: 'web-1',
  scope: 'tuple',
  check_key: 'disk',
  item: '/var',
  metric: 'use_pct',
  opened_at: '2026-10-01T00:00:00Z',
  severity: 'warn',
  peak_severity: 'crit',
  snooze_until_change: false,
  was_flapping: false,
  muted: false,
  ...over,
});

function renderCard(canRead = true) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <HostAlertsCard org="acme" hostId="h1" canRead={canRead} />
    </QueryClientProvider>,
  );
}

describe('HostAlertsCard', () => {
  it('reads the list endpoint filtered by host, not a host-specific route', async () => {
    let askedSearch: Record<string, string> | undefined;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': (req: MockRequest) => {
        askedSearch = Object.fromEntries(req.search.entries());
        return { alerts: [alert()] };
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    // The request that actually landed carries `host=h1` as a query
    // parameter on the shared list route — a component that instead (or
    // additionally) called a made-up host-specific endpoint would either
    // 404 against this mock (there is no route registered for it) or
    // leave `askedSearch` unset for this one, so this assertion is the one
    // that would fail if a seventh route were invented.
    expect(askedSearch?.host).toBe('h1');
  });

  it('renders an explicit empty state when the host has no alerts (it is healthy)', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/alerts': { alerts: [] } });
    renderCard();

    expect(await screen.findByText(/healthy/i)).toBeInTheDocument();
    expect(screen.queryByTestId('alert-open')).not.toBeInTheDocument();
  });

  it('renders the three facts for each of this host\'s alerts: open-ness, peak severity, tracked severity', async () => {
    mockApi({
      'GET /api/v1/orgs/acme/alerts': {
        alerts: [alert({ severity: 'warn', peak_severity: 'crit', closed_at: undefined })],
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    expect(screen.getByTestId('alert-open')).toHaveTextContent(/open/i);
    expect(screen.getByTestId('alert-peak')).toHaveTextContent(/crit/i);
    expect(screen.getByTestId('alert-current')).toHaveTextContent(/warn/i);
  });

  it('asks for nothing without checks.read', async () => {
    let asked = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': () => {
        asked = true;
        return { alerts: [] };
      },
    });
    renderCard(false);

    expect(await screen.findByText(/visible to members/i)).toBeInTheDocument();
    expect(asked).toBe(false);
  });

  it('shows a read error rather than hanging on "Loading…" forever', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/alerts': () => new Response('{"detail":"boom"}', { status: 500 }) });
    renderCard();

    expect(await screen.findByRole('alert')).toHaveTextContent(/boom/i);
  });

  it('fetches one alert\'s timeline from the detail endpoint only once its disclosure is opened, not from a second list call', async () => {
    const user = userEvent.setup();
    let detailRequested = false;
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert()] },
      'GET /api/v1/orgs/acme/alerts/a1': () => {
        detailRequested = true;
        return {
          alert: {
            ...alert(),
            timeline: [{ at: '2026-10-01T00:00:00Z', kind: 'event', event: { id: 'e1', at: '2026-10-01T00:00:00Z', kind: 'opened' } }],
          },
        };
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    expect(detailRequested).toBe(false);

    await user.click(screen.getByRole('button', { name: /show timeline/i }));
    expect(await screen.findByText(/alert opened/i)).toBeInTheDocument();
    expect(detailRequested).toBe(true);
  });

  it('renders a host-scope alert\'s timeline (events only, no state history) without an error', async () => {
    const user = userEvent.setup();
    mockApi({
      'GET /api/v1/orgs/acme/alerts': { alerts: [alert({ scope: 'host', check_key: undefined, item: undefined, metric: undefined })] },
      'GET /api/v1/orgs/acme/alerts/a1': {
        alert: {
          ...alert({ scope: 'host', check_key: undefined, item: undefined, metric: undefined }),
          timeline: [{ at: '2026-10-01T00:00:00Z', kind: 'event', event: { id: 'e1', at: '2026-10-01T00:00:00Z', kind: 'opened' } }],
        },
      },
    });
    renderCard();

    await screen.findByTestId('alert-open');
    await user.click(screen.getByRole('button', { name: /show timeline/i }));

    expect(await screen.findByText(/alert opened/i)).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
