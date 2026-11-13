import { describe, expect, it } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { I18nProvider } from '../../i18n/I18nProvider.tsx';
import { MetricGraphPanel } from './MetricGraphPanel.tsx';

const NOW = '2026-09-27T12:00:00Z';

describe('MetricGraphPanel', () => {
  // The offered ranges follow the organization's effective retention, which
  // can fall while a long range is selected: the panel then shows and asks
  // for 7 d instead of a window no button offers.
  it('falls back to 7 d when the selected range is no longer offered', async () => {
    const asked: string[] = [];
    mockApi({
      'GET /api/v1/orgs/acme/settings/monitoring': {
        settings: { client_absent_days: 14, retention_days: 30, retention_max_days: 365, retention_effective_days: 30 },
      },
      'GET /api/v1/orgs/acme/hosts/h1/metrics': (req: MockRequest) => {
        asked.push(req.search.get('range') ?? '');
        return { check: 'cpu', item: '', metric: 'usage_percent', range: req.search.get('range'), from: NOW, to: NOW, points: [] };
      },
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider>
          <MetricGraphPanel
            org="acme"
            hostId="h1"
            label="cpu usage_percent"
            selection={{ check: 'cpu', item: '', metric: 'usage_percent', range: '90d' }}
            onRange={() => {}}
          />
        </I18nProvider>
      </QueryClientProvider>,
    );

    await waitFor(() => expect(screen.getByRole('button', { name: '30 d' })).toBeInTheDocument());
    await waitFor(() => expect(screen.getByRole('button', { name: '7 d' })).toHaveAttribute('aria-pressed', 'true'));
    expect(screen.queryByRole('button', { name: '90 d' })).not.toBeInTheDocument();
    await waitFor(() => expect(asked[asked.length - 1]).toBe('7d'));
  });
});
