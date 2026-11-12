import { describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { ConfirmProvider } from '../../ui/confirm.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ForgetItemButton } from './ForgetItemButton.tsx';

function renderButton(item = '/mnt/a b?x=1') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidate = vi.spyOn(qc, 'invalidateQueries');
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ConfirmProvider>
          <ForgetItemButton org="acme" hostId="h1" checkKey="disk" item={item} />
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { qc, invalidate };
}

const confirmDialog = (item: string) => screen.findByRole('dialog', { name: `Forget ${item}?` });

describe('ForgetItemButton', () => {
  it('asks first, naming the item and what forgetting removes and keeps', async () => {
    const user = userEvent.setup();
    let deleted = false;
    mockApi({
      'DELETE /api/v1/orgs/acme/hosts/h1/items': () => {
        deleted = true;
        return new Response(null, { status: 204 });
      },
    });
    renderButton('/mnt/old');

    await user.click(screen.getByRole('button', { name: 'Forget disk /mnt/old' }));
    const dialog = await confirmDialog('/mnt/old');
    expect(
      within(dialog).getByText('Its current values and open alerts are removed; its graphs stay. If it reports again it comes back.'),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(deleted).toBe(false);
  });

  it('on confirm, sends DELETE with the check key and the item in the query, then refreshes the host page', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi({
      'DELETE /api/v1/orgs/acme/hosts/h1/items': (req: MockRequest) => {
        seen = req;
        return new Response(null, { status: 204 });
      },
    });
    const { invalidate } = renderButton('/mnt/a b?x=1');

    await user.click(screen.getByRole('button', { name: 'Forget disk /mnt/a b?x=1' }));
    const dialog = await confirmDialog('/mnt/a b?x=1');
    await user.click(within(dialog).getByRole('button', { name: 'Forget' }));

    await waitFor(() => expect(seen).toBeDefined());
    expect(seen!.search.get('check_key')).toBe('disk');
    expect(seen!.search.get('item')).toBe('/mnt/a b?x=1');
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ['host-checks', 'acme', 'h1'] }));
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['alerts', 'acme'] });
    expect(await screen.findByText('/mnt/a b?x=1 forgotten')).toBeInTheDocument();
  });

  it('sends item= for the whole-host item rather than leaving it out', async () => {
    const user = userEvent.setup();
    let seen: MockRequest | undefined;
    mockApi({
      'DELETE /api/v1/orgs/acme/hosts/h1/items': (req: MockRequest) => {
        seen = req;
        return new Response(null, { status: 204 });
      },
    });
    renderButton('');

    await user.click(screen.getByRole('button', { name: /^Forget disk/ }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Forget' }));

    await waitFor(() => expect(seen).toBeDefined());
    expect(seen!.search.has('item')).toBe(true);
    expect(seen!.search.get('item')).toBe('');
  });

  // A 404 most likely means the hourly expiry forgot the item first: the
  // error is still said, and the host page is still read again, so the
  // row that is already gone on the server leaves the page too.
  it('says so when the forget fails, and still refreshes the host page', async () => {
    const user = userEvent.setup();
    mockApi({
      'DELETE /api/v1/orgs/acme/hosts/h1/items': () =>
        new Response(JSON.stringify({ detail: 'not found' }), { status: 404, headers: { 'Content-Type': 'application/problem+json' } }),
    });
    const { invalidate } = renderButton('/mnt/old');

    await user.click(screen.getByRole('button', { name: 'Forget disk /mnt/old' }));
    const dialog = await confirmDialog('/mnt/old');
    await user.click(within(dialog).getByRole('button', { name: 'Forget' }));

    expect(await screen.findByText('not found')).toBeInTheDocument();
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['host-checks', 'acme', 'h1'] });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['host-items', 'acme', 'h1'] });
  });
});
