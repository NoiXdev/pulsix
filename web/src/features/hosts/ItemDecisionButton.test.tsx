import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ItemDecisionButton } from './ItemDecisionButton.tsx';
import type { ItemDecision } from '../../api/types.ts';

const orgDecision = (over: Partial<ItemDecision> = {}): ItemDecision => ({
  id: 'org-dec',
  scope: 'org',
  check_key: 'disk',
  match_kind: 'glob',
  pattern: '/mnt/scratch-*',
  decision: 'ignore',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  ...over,
});

function renderButton(props: Partial<React.ComponentProps<typeof ItemDecisionButton>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ItemDecisionButton org="acme" hostId="h1" checkKey="disk" item="/var" ignored={false} {...props} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return qc;
}

describe('ItemDecisionButton', () => {
  it('posts a host-scoped exact ignore decision for an item nothing currently decides', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
        posted = req.body;
        return { decision: { id: 'new', ...( req.body as object), created_at: '', updated_at: '' } };
      },
    });
    renderButton({ ignored: false });

    await user.click(screen.getByRole('button', { name: 'Ignore disk /var' }));

    expect(posted).toEqual({
      scope: 'host',
      host_id: 'h1',
      check_key: 'disk',
      match_kind: 'exact',
      pattern: '/var',
      decision: 'ignore',
    });
  });

  // Review focus 3: an organization pattern ignores this item, and the
  // operator clicks "Watch" on the row. The button must not edit the
  // organization decision (it is not this button's to touch, and it
  // governs other items too) — it must POST a brand new host-scoped exact
  // decision, which outranks the org pattern regardless of match kind.
  it('posts a NEW host-scoped exact watch decision to overrule an organization pattern, rather than editing it', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    let putCalled = false;
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => {
        posted = req.body;
        return { decision: { id: 'new', ...(req.body as object), created_at: '', updated_at: '' } };
      },
      'PUT /api/v1/orgs/acme/item-decisions/org-dec': () => {
        putCalled = true;
        return new Response(JSON.stringify({ detail: 'must not touch the org decision' }), { status: 500 });
      },
    });
    renderButton({ ignored: true, decidedBy: orgDecision() });

    await user.click(screen.getByRole('button', { name: 'Watch disk /var' }));

    expect(putCalled).toBe(false);
    expect(posted).toEqual({
      scope: 'host',
      host_id: 'h1',
      check_key: 'disk',
      match_kind: 'exact',
      pattern: '/var',
      decision: 'watch',
    });
  });

  // Clicking the same row's button a second time must not stack a second
  // host+exact decision beside the first: internal/items' own tie-break
  // for two decisions at the same specificity is the OLDER created_at, so
  // a second POST would silently lose to the first and leave the item
  // exactly where it was before the click.
  it('updates its own prior host-scoped exact decision instead of creating a second one', async () => {
    const user = userEvent.setup();
    let putBody: unknown;
    let postCalled = false;
    const own = orgDecision({
      id: 'own-dec',
      scope: 'host',
      host_id: 'h1',
      match_kind: 'exact',
      pattern: '/var',
      decision: 'ignore',
      note: 'Known noisy volume.',
    });
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions': () => {
        postCalled = true;
        return new Response(JSON.stringify({ detail: 'must not create a second decision' }), { status: 500 });
      },
      'PUT /api/v1/orgs/acme/item-decisions/own-dec': (req: MockRequest) => {
        putBody = req.body;
        return { decision: { ...own, decision: 'watch' } };
      },
    });
    renderButton({ ignored: true, decidedBy: own });

    await user.click(screen.getByRole('button', { name: 'Watch disk /var' }));

    expect(postCalled).toBe(false);
    expect(putBody).toEqual({
      scope: 'host',
      host_id: 'h1',
      check_key: 'disk',
      match_kind: 'exact',
      pattern: '/var',
      decision: 'watch',
      note: 'Known noisy volume.',
    });
  });

  it('reports a failed save instead of leaving the click looking like it did nothing', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions': () => new Response(JSON.stringify({ detail: 'saving failed' }), { status: 500 }),
    });
    renderButton({ ignored: false });

    await user.click(screen.getByRole('button', { name: 'Ignore disk /var' }));

    expect(await screen.findByText('saving failed')).toBeInTheDocument();
  });

  it('refreshes the host’s checks, items and effective rules after a successful save', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/item-decisions': (req: MockRequest) => ({
        decision: { id: 'new', ...(req.body as object), created_at: '', updated_at: '' },
      }),
    });
    const qc = renderButton({ ignored: false });
    const invalidated: unknown[] = [];
    const original = qc.invalidateQueries.bind(qc);
    qc.invalidateQueries = (filters) => {
      invalidated.push(filters?.queryKey);
      return original(filters);
    };

    await user.click(screen.getByRole('button', { name: 'Ignore disk /var' }));

    await screen.findByRole('button', { name: 'Ignore disk /var' });
    expect(invalidated).toContainEqual(['host-checks', 'acme', 'h1']);
    expect(invalidated).toContainEqual(['host-items', 'acme', 'h1']);
    expect(invalidated).toContainEqual(['host-effective-rules', 'acme', 'h1']);
  });
});
