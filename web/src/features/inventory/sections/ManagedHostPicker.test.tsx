import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { mockApi, type MockRequest } from '../../../test/mockApi.ts';
import { ManagedHostPicker } from './ManagedHostPicker.tsx';

function renderPicker(existing: readonly string[] = []) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ManagedHostPicker org="acme" existing={existing} value="" onChange={() => undefined} />
    </QueryClientProvider>,
  );
}

function serverError() {
  return new Response(JSON.stringify({ status: 500, detail: 'boom' }), { status: 500, headers: { 'Content-Type': 'application/json' } });
}

const host = (id: string, name: string) => ({ id, name, zone_id: 'z1', address: '', managed: true, monitored: false, facts: {}, created_at: '' });

describe('ManagedHostPicker', () => {
  it('shows an error state when the first page fails', async () => {
    mockApi({ 'GET /api/v1/orgs/acme/hosts': serverError });
    renderPicker();
    expect(await screen.findByText(/Could not load managed hosts/i)).toBeInTheDocument();
    expect(screen.queryByText('Every managed host is already a member.')).not.toBeInTheDocument();
  });

  it('stops auto-loading and shows the error after a later page fails, instead of retrying in a loop', async () => {
    let page2Requests = 0;
    mockApi({
      'GET /api/v1/orgs/acme/hosts': (req: MockRequest) => {
        if (!req.search.get('cursor')) return { hosts: [host('h1', 'web-01')], next_cursor: 'p2' };
        page2Requests++;
        return serverError();
      },
    });
    // h1 is already a member, so page 1 alone has zero assignable hosts —
    // this is what makes ManagedHostPicker auto-fetch the next page.
    renderPicker(['h1']);
    expect(await screen.findByText(/Could not load more managed hosts/i)).toBeInTheDocument();
    const afterFirstFailure = page2Requests;
    expect(afterFirstFailure).toBe(1);
    // Give any runaway retry effect a chance to fire before asserting it
    // did not.
    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(page2Requests).toBe(afterFirstFailure);
  });
});
