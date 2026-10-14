import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ToastProvider } from '../../ui/toast.tsx';
import { mockApi, type MockRequest } from '../../test/mockApi.ts';
import { ZoneDialog } from './ZoneDialog.tsx';

function renderDialog(over: Partial<React.ComponentProps<typeof ZoneDialog>> = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onClose = vi.fn();
  const onCreated = vi.fn();
  render(
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <ZoneDialog org="acme" open onClose={onClose} onCreated={onCreated} {...over} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { onClose, onCreated };
}

// These cases lived in HostsListPage.test.tsx's "Add zone" dialog before the
// v0.4g Task 7 extraction — moved here unchanged in spirit (the dialog is
// now rendered directly rather than through the page's own "Add zone"
// button), plus the new prefill case the extraction exists to serve.
describe('ZoneDialog', () => {
  it('creates a zone and reports its id', async () => {
    const user = userEvent.setup();
    let posted: unknown;
    mockApi({
      'POST /api/v1/orgs/acme/zones': (req: MockRequest) => {
        posted = req.body;
        return { zone: { id: 'z9', slug: 'frankfurt', name: 'Frankfurt' } };
      },
    });
    const { onClose, onCreated } = renderDialog();

    await user.type(screen.getByLabelText('Name'), 'Frankfurt');
    await user.type(screen.getByLabelText('Slug'), 'frankfurt');
    await user.click(screen.getByRole('button', { name: 'Create zone' }));

    expect(await screen.findByText('Zone Frankfurt created')).toBeInTheDocument();
    expect(posted).toEqual({ name: 'Frankfurt', slug: 'frankfurt' });
    expect(onCreated).toHaveBeenCalledWith('z9');
    expect(onClose).toHaveBeenCalled();
  });

  it('derives the slug from the name once the slug field is tabbed past empty', async () => {
    const user = userEvent.setup();
    mockApi({ 'POST /api/v1/orgs/acme/zones': { zone: { id: 'z9', slug: 'frankfurt', name: 'Frankfurt' } } });
    renderDialog();

    await user.type(screen.getByLabelText('Name'), 'Frankfurt');
    // SlugField only fills itself on its OWN blur (ui/SlugField.tsx) —
    // tabbing from Name lands focus on Slug first; a second tab blurs it.
    await user.tab();
    await user.tab();
    expect(screen.getByLabelText('Slug')).toHaveValue('frankfurt');
  });

  it('shows a required-name error and does not submit', async () => {
    const user = userEvent.setup();
    const posted: unknown[] = [];
    mockApi({
      'POST /api/v1/orgs/acme/zones': (req: MockRequest) => {
        posted.push(req.body);
        return { zone: { id: 'z9', slug: '', name: '' } };
      },
    });
    renderDialog();

    await user.click(screen.getByRole('button', { name: 'Create zone' }));

    // Both Name and Slug are required and both are empty, so two fields
    // report it — this only cares that validation blocked the submit.
    expect((await screen.findAllByText('required')).length).toBeGreaterThan(0);
    expect(posted).toEqual([]);
  });

  it('reports a server failure as a toast and keeps the dialog open', async () => {
    const user = userEvent.setup();
    mockApi({
      'POST /api/v1/orgs/acme/zones': () =>
        new Response(JSON.stringify({ status: 409, detail: 'slug already in use' }), { status: 409, headers: { 'Content-Type': 'application/json' } }),
    });
    const { onClose } = renderDialog();

    await user.type(screen.getByLabelText('Name'), 'Frankfurt');
    await user.type(screen.getByLabelText('Slug'), 'frankfurt');
    await user.click(screen.getByRole('button', { name: 'Create zone' }));

    expect(await screen.findByText('slug already in use')).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('prefills the name it was given, so a picker can hand over what was typed', async () => {
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ToastProvider>
          <ZoneDialog org="acme" open onClose={vi.fn()} onCreated={vi.fn()} initialName="frankfurt" />
        </ToastProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByLabelText('Name')).toHaveValue('frankfurt');
  });
});
